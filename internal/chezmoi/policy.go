package chezmoi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"dotm/internal/manifest"
)

// PolicyInput identifies the selected dotm configuration independently of package readiness.
type PolicyInput struct {
	Repository    string
	Configuration string
	SelectionPath string
	Applications  []string
	Files         manifest.FileModel
}

// PolicyExclusion retains acknowledgement/profile provenance for later lint and inventory.
type PolicyExclusion struct {
	Root   string                   `json:"root"`
	Origin manifest.FileDeclaration `json:"origin"`
}

// Policy is literal external template data, never executable template text.
// Freshness is a local snapshot, not a guarantee about environment-dependent templates.
type Policy struct {
	SchemaVersion int               `json:"schemaVersion"`
	Repository    string            `json:"repository"`
	SourceDir     string            `json:"sourceDir"`
	DestDir       string            `json:"destDir"`
	ConfigFile    string            `json:"configFile"`
	Patterns      []string          `json:"patterns"`
	Exclusions    []PolicyExclusion `json:"exclusions"`
	Configuration string            `json:"configuration"`
	Freshness     string            `json:"freshness"`
	Input         PolicyInput       `json:"input"`
	Source        string            `json:"source"`
}

// PolicyPath returns the one fixed policy location for the effective state directory.
// An explicitly configured relative XDG_STATE_HOME is unsupported, never rebased.
func PolicyPath(_ Policy) (string, error) {
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		state = os.Getenv("HOME")
		if state == "" || !filepath.IsAbs(state) {
			return "", fmt.Errorf("HOME must be an absolute path when XDG_STATE_HOME is unset")
		}
		state = filepath.Join(state, ".local", "state")
	} else if !filepath.IsAbs(state) {
		return "", fmt.Errorf("XDG_STATE_HOME must be an absolute path")
	}
	return filepath.Join(filepath.Clean(state), "dotm", "chezmoi-policy.json"), nil
}

// CompilePolicy validates passive safety before evaluating trusted chezmoi templates.
// It neither fetches nor publishes, and deliberately bypasses any stale published policy.
func CompilePolicy(ctx context.Context, source string, input PolicyInput) (Policy, error) {
	var policy Policy
	if input.Repository == "" || input.Configuration == "" {
		return policy, fmt.Errorf("policy requires configured repository URL and dotm configuration identity")
	}
	if err := input.Files.Err(); err != nil {
		return policy, err
	}
	validation, err := ValidateSource(ctx, source)
	if err != nil {
		return policy, err
	}
	if err = validation.Err(); err != nil {
		return policy, fmt.Errorf("policy source validation failed: %w", err)
	}
	if err = ValidateRepository(ctx, source, input.Repository); err != nil {
		return policy, fmt.Errorf("policy source does not match configured repository: %w", err)
	}
	origin, err := git(ctx, source, "remote", "get-url", "--all", "origin")
	if err != nil {
		return policy, fmt.Errorf("inspect source origin: %w", err)
	}
	command := applyCommand{source: source}
	native := `{"dotm":{"nativeOnly":true}}`
	metadata, err := policyCommand(ctx, command, "--override-data", native, "execute-template",
		`{{ dict "sourceDir" .chezmoi.sourceDir "destDir" .chezmoi.destDir `+
			`"configFile" .chezmoi.configFile | toJson }}`)
	if err != nil {
		return policy, err
	}
	if err = json.Unmarshal(metadata, &policy); err != nil {
		return policy, fmt.Errorf("decode policy binding: %w", err)
	}
	settings, err := policyCommand(ctx, command, "--override-data", native, "dump-config", "--format=json")
	if err != nil {
		return policy, err
	}
	var effective struct {
		Mode string `json:"mode"`
	}
	if err = json.Unmarshal(settings, &effective); err != nil {
		return policy, fmt.Errorf("decode policy settings: %w", err)
	}
	for _, path := range []string{policy.SourceDir, policy.DestDir, policy.ConfigFile} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return policy, fmt.Errorf("unsupported effective policy binding path %q", path)
		}
	}
	if effective.Mode != "file" {
		return policy, fmt.Errorf("files policy requires chezmoi mode=file")
	}
	policy.Repository = origin
	policy.SchemaVersion = 1
	policy.Configuration = input.Configuration
	policy.Source, err = filepath.Abs(source)
	if err != nil {
		return policy, err
	}
	policy.Input = input
	policy.Patterns = []string{}
	policy.Exclusions = []PolicyExclusion{}
	patterns := make(map[string]bool)
	for _, declaration := range input.Files.Declarations {
		if declaration.Field != "configs" && declaration.Field != "unmanaged" {
			return policy, fmt.Errorf("unsupported file declaration: %s", declaration)
		}
		// Diagnose mappings for selected configs too, even though they do not generate rules.
		path := declaration.Path
		if !utf8.ValidString(path) || strings.ContainsRune(path, '\x00') ||
			!filepath.IsAbs(path) || filepath.Clean(path) != path {
			return policy, fmt.Errorf("invalid policy declaration path: %s", declaration)
		}
		if !within(policy.DestDir, path) || path == policy.DestDir {
			return policy, fmt.Errorf("policy path is outside or equals effective destination: %s; "+
				"XDG expansion does not relocate source entries", declaration)
		}
		if (declaration.Field == "configs" && declaration.Selected) ||
			(declaration.Field == "unmanaged" && !declaration.Selected && declaration.Application != "") {
			continue
		}
		if strings.ContainsAny(path, "\r\n") {
			return policy, fmt.Errorf("unrepresentable generated policy path (CR/LF): %s", declaration)
		}
		relative, relErr := filepath.Rel(policy.DestDir, path)
		if relErr != nil {
			return policy, relErr
		}
		policy.Exclusions = append(policy.Exclusions, PolicyExclusion{Root: path, Origin: declaration})
		pattern := escapePolicyPath(filepath.ToSlash(relative))
		patterns[pattern], patterns[pattern+"/**"] = true, true
	}
	for pattern := range patterns {
		policy.Patterns = append(policy.Patterns, pattern)
	}
	sort.Strings(policy.Patterns)
	policy.Freshness, err = policyFreshness(ctx, policy, settings)
	return policy, err
}

func escapePolicyPath(path string) string {
	var escaped strings.Builder
	for i, char := range path {
		switch char {
		case '#':
			escaped.WriteString("[#]")
		case ' ', '\t':
			escaped.WriteRune('[')
			escaped.WriteRune(char)
			escaped.WriteRune(']')
		case '\\', '*', '?', '[', ']', '{', '}':
			escaped.WriteRune('\\')
			escaped.WriteRune(char)
		case '!':
			if i == 0 {
				escaped.WriteRune('\\')
			}
			escaped.WriteRune(char)
		default:
			escaped.WriteRune(char)
		}
	}
	return escaped.String()
}

func policyCommand(ctx context.Context, command applyCommand, args ...string) ([]byte, error) {
	data, message, err := command.run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("chezmoi policy inspection: %w: %s", err, message)
	}
	return data, nil
}

// Hash every checkout input rather than trying to predict template dependencies.
// Symlinks are hashed as links, never traversed; Git metadata is represented by HEAD/origin.
func policyFreshness(ctx context.Context, policy Policy, settings []byte) (string, error) {
	head, err := git(ctx, policy.Source, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	origin, err := git(ctx, policy.Source, "remote", "get-url", "origin")
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	inputs := []any{policy.Input, repositoryIdentity(origin), head, json.RawMessage(settings)}
	if err = encoder.Encode(inputs); err != nil {
		return "", err
	}
	err = filepath.WalkDir(policy.Source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		if entry.Name() == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		var content []byte
		var readErr error
		if entry.Type()&os.ModeSymlink != 0 {
			var target string
			target, readErr = os.Readlink(path)
			content = []byte(target)
		} else if entry.Type().IsRegular() {
			// Caller holds mutation ownership; concurrent manual source changes are unsupported.
			//nolint:gosec // G122: trusted checkout input, no writes; WalkDir does not follow symlink entries.
			content, readErr = os.ReadFile(path)
		} else {
			return fmt.Errorf("unsupported policy input node %s", path)
		}
		if readErr != nil {
			return readErr
		}
		relative, relErr := filepath.Rel(policy.Source, path)
		if relErr != nil {
			return relErr
		}
		return encoder.Encode([]any{relative, entry.Type().String(), content})
	})
	if err != nil {
		return "", err
	}
	config, err := os.ReadFile(policy.ConfigFile)
	if err != nil {
		return "", err
	}
	if err = encoder.Encode(config); err != nil {
		return "", err
	}
	if policy.Input.SelectionPath != "" {
		selection, readErr := os.ReadFile(policy.Input.SelectionPath)
		if readErr != nil {
			return "", readErr
		}
		if err = encoder.Encode(selection); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// PublishPolicy checks a fresh candidate before atomic replacement. Callers hold
// mutation ownership and run CheckPolicy before and after publication. Post-publication
// manual mutation and power-loss rollback are not promised.
func PublishPolicy(ctx context.Context, source string, policy Policy) error {
	current, err := CompilePolicy(ctx, source, policy.Input)
	if err != nil {
		return err
	}
	want, err := json.Marshal(current)
	if err != nil {
		return err
	}
	got, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	if !bytes.Equal(want, got) {
		return fmt.Errorf("policy candidate is stale or modified; compile it again")
	}
	path, err := PolicyPath(policy)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	candidate, err := os.CreateTemp(filepath.Dir(path), ".candidate-*.json")
	if err != nil {
		return err
	}
	candidatePath := candidate.Name()
	defer os.Remove(candidatePath)
	// A unique literal marker proves inclusion even when the real policy is empty.
	probe := policy
	marker := ".dotm-policy-probe-" + policy.Freshness
	probe.Patterns = append(append([]string{}, policy.Patterns...), marker)
	writeErr := json.NewEncoder(candidate).Encode(probe)
	closeErr := candidate.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	override, err := json.Marshal(map[string]any{"dotm": map[string]any{
		"policyCandidate": candidatePath, "nativeOnly": false,
	}})
	if err != nil {
		return err
	}
	command := applyCommand{source: source}
	rendered, err := policyCommand(ctx, command, "--override-data", string(override),
		"execute-template", "--file", filepath.Join(policy.SourceDir, ".chezmoiignore"))
	if err != nil {
		return err
	}
	found := false
	for _, line := range strings.Split(string(rendered), "\n") {
		if line == marker {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("root .chezmoiignore does not consume the fixed-path external policy")
	}
	if err = os.WriteFile(candidatePath, want, 0o600); err != nil {
		return err
	}
	if _, err = policyCommand(ctx, command, "--override-data", string(override), "dump", "--format=json"); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = os.Rename(candidatePath, path); err != nil {
		return fmt.Errorf("publish external chezmoi policy: %w", err)
	}
	if _, err = policyCommand(ctx, command, "dump", "--format=json"); err != nil {
		return fmt.Errorf("policy published, but normal integration inspection failed: %w", err)
	}
	return nil
}

// CheckPolicy checks native effective rules before publication and verifies the
// persisted binding and normal integration when published is true.
func CheckPolicy(ctx context.Context, source string, policy Policy, published bool) (manifest.FileModel, error) {
	result := manifest.FileModel{}
	candidate, err := CompilePolicy(ctx, source, policy.Input)
	if err != nil {
		return result, err
	}
	if published {
		path, pathErr := PolicyPath(candidate)
		if pathErr != nil {
			return result, pathErr
		}
		info, statErr := os.Lstat(path)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				result.Diagnostics = append(result.Diagnostics, manifest.FileDiagnostic{
					Severity: manifest.FileError, Path: path,
					Message: "published files policy is missing; run dotm files refresh",
				})
			} else {
				return result, fmt.Errorf("inspect published files policy: %w", statErr)
			}
		} else if !info.Mode().IsRegular() {
			result.Diagnostics = append(result.Diagnostics, manifest.FileDiagnostic{
				Severity: manifest.FileError, Path: path,
				Message: "published files policy is not a regular file; correct the policy path before refreshing",
			})
		} else {
			persistedBytes, readErr := os.ReadFile(path)
			if readErr != nil {
				return result, fmt.Errorf("read published files policy: %w", readErr)
			}
			var persisted Policy
			if decodeErr := json.Unmarshal(persistedBytes, &persisted); decodeErr != nil {
				result.Diagnostics = append(result.Diagnostics, manifest.FileDiagnostic{
					Severity: manifest.FileError, Path: path,
					Message: fmt.Sprintf("published files policy is malformed: %v; run dotm files refresh", decodeErr),
				})
			} else {
				want, marshalErr := json.Marshal(candidate)
				if marshalErr != nil {
					return result, marshalErr
				}
				persistedJSON, marshalErr := json.Marshal(persisted)
				if marshalErr != nil {
					return result, marshalErr
				}
				callerJSON, marshalErr := json.Marshal(policy)
				if marshalErr != nil {
					return result, marshalErr
				}
				if !bytes.Equal(want, persistedJSON) || !bytes.Equal(want, callerJSON) {
					result.Diagnostics = append(result.Diagnostics, manifest.FileDiagnostic{
						Severity: manifest.FileError, Path: path,
						Message: "published files policy is stale or mismatched; run dotm files refresh",
					})
				}
			}
		}
	}
	nativeDiagnostics, err := inspectEffectiveRules(ctx, source, candidate)
	if err != nil {
		return result, err
	}
	result.Diagnostics = append(result.Diagnostics, nativeDiagnostics...)
	consumed, candidateManaged, probeErr := verifyPolicyConsumption(ctx, applyCommand{source: source}, candidate)
	if probeErr != nil {
		return result, probeErr
	}
	if !consumed {
		path, pathErr := PolicyPath(candidate)
		if pathErr != nil {
			return result, pathErr
		}
		result.Diagnostics = append(result.Diagnostics, manifest.FileDiagnostic{
			Severity: manifest.FileError, Path: filepath.Join(candidate.SourceDir, ".chezmoiignore"),
			Message: fmt.Sprintf("root ignore template does not consume external policy %q; "+
				"deliberately adopt examples/chezmoi-policy/.chezmoiignore; "+
				"dotm does not edit user integration files", path),
		})
		return result, nil
	}
	command := applyCommand{source: source}
	nativeData := `{"dotm":{"nativeOnly":true}}`
	native, err := policyCommand(ctx, command, "--override-data", nativeData, "managed", "--nul-path-separator")
	if err != nil {
		return result, fmt.Errorf("inspect native managed files: %w", err)
	}
	if published {
		full, fullErr := policyCommand(ctx, command, "managed", "--nul-path-separator")
		if fullErr != nil {
			return result, fmt.Errorf("inspect published managed files: %w", fullErr)
		}
		for _, sourcePath := range strings.Split(strings.TrimSuffix(string(full), "\x00"), "\x00") {
			for _, exclusion := range candidate.Exclusions {
				root, rootErr := filepath.Rel(candidate.DestDir, exclusion.Root)
				if rootErr != nil {
					return result, rootErr
				}
				root = filepath.ToSlash(root)
				if filepath.ToSlash(sourcePath) == root || strings.HasPrefix(filepath.ToSlash(sourcePath), root+"/") {
					result.Diagnostics = append(result.Diagnostics, manifest.FileDiagnostic{
						Severity: manifest.FileError, Declaration: exclusion.Origin,
						Message: fmt.Sprintf("published policy integration leaves excluded managed path %q visible",
							sourcePath),
					})
				}
			}
		}
	}
	if published {
		ignored, ignoredErr := policyCommand(ctx, command, "ignored", "--nul-path-separator")
		if ignoredErr != nil {
			return result, fmt.Errorf("inspect published ignored files: %w", ignoredErr)
		}
		ignoredSet := make(map[string]bool)
		for _, path := range strings.Split(strings.TrimSuffix(string(ignored), "\x00"), "\x00") {
			ignoredSet[filepath.ToSlash(path)] = true
		}
		for _, sourcePath := range strings.Split(strings.TrimSuffix(string(native), "\x00"), "\x00") {
			for _, exclusion := range candidate.Exclusions {
				root, rootErr := filepath.Rel(candidate.DestDir, exclusion.Root)
				if rootErr != nil {
					return result, rootErr
				}
				root = filepath.ToSlash(root)
				path := filepath.ToSlash(sourcePath)
				if (path == root || strings.HasPrefix(path, root+"/")) && !isIgnoredByAncestor(path, ignoredSet) {
					result.Diagnostics = append(result.Diagnostics, manifest.FileDiagnostic{
						Severity: manifest.FileError, Declaration: exclusion.Origin,
						Message: fmt.Sprintf("published ignored output omits native path %q "+
							"under a generated exclusion",
							path),
					})
				}
			}
		}
	}
	var mappedSourceTargets []string
	sourceTargetsMapped := false
	for _, declaration := range candidate.Input.Files.Declarations {
		if declaration.Field != "configs" || !declaration.Selected {
			continue
		}
		visibleRoot := filepath.ToSlash(filepath.Clean(declaration.Path))
		visible := false
		for _, sourcePath := range strings.Split(strings.TrimSuffix(string(candidateManaged), "\x00"), "\x00") {
			path := filepath.ToSlash(filepath.Clean(filepath.Join(candidate.DestDir, filepath.FromSlash(sourcePath))))
			if path == visibleRoot || strings.HasPrefix(path, visibleRoot+"/") {
				visible = true
				break
			}
		}
		if visible {
			continue
		}
		if !sourceTargetsMapped {
			entries, walkErr := effectiveSourceEntries(candidate.SourceDir)
			if walkErr != nil {
				return result, fmt.Errorf("inspect source members for hidden configs: %w", walkErr)
			}
			for _, entry := range entries {
				if filepath.Base(entry) == ".chezmoiignore" {
					continue
				}
				target, targetErr := policyCommand(ctx, command, "--override-data", nativeData, "target-path", entry)
				if targetErr != nil {
					return result, fmt.Errorf("map source entry %q for hidden-config inspection: %w", entry, targetErr)
				}
				targetPath := strings.TrimSuffix(string(target), "\n")
				if targetPath == "" {
					return result, fmt.Errorf("empty target mapping for source entry %q", entry)
				}
				if !filepath.IsAbs(targetPath) {
					return result, fmt.Errorf("non-absolute target mapping for source entry %q: %q", entry, targetPath)
				}
				mappedSourceTargets = append(mappedSourceTargets, filepath.Clean(targetPath))
			}
			sourceTargetsMapped = true
		}
		for _, target := range mappedSourceTargets {
			if target == visibleRoot || within(visibleRoot, target) {
				result.Diagnostics = append(result.Diagnostics, manifest.FileDiagnostic{
					Severity: manifest.FileWarning, Declaration: declaration,
					Message: "selected config is wholly hidden by native ignore rules",
				})
				break
			}
		}
	}
	if published {
		if _, runErr := policyCommand(ctx, command, "dump", "--format=json"); runErr != nil {
			return result, fmt.Errorf("inspect published files policy integration: %w", runErr)
		}
	}
	return result, nil
}

// verifyPolicyConsumption uses an isolated scratch policy carrying a unique
// literal marker. It never changes published state, source, or destinations.
func verifyPolicyConsumption(ctx context.Context, command applyCommand, policy Policy) (bool, []byte, error) {
	if _, err := os.Lstat(filepath.Join(policy.SourceDir, ".chezmoiignore")); err != nil {
		if os.IsNotExist(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	marker := ".dotm-policy-probe-" + policy.Freshness
	probe := policy
	probe.Patterns = append(append([]string(nil), policy.Patterns...), marker)
	data, err := json.Marshal(probe)
	if err != nil {
		return false, nil, err
	}
	file, err := os.CreateTemp("", "dotm-policy-probe-*.json")
	if err != nil {
		return false, nil, fmt.Errorf("create policy inspection probe: %w", err)
	}
	path := file.Name()
	defer os.Remove(path)
	if _, err = file.Write(data); err != nil {
		_ = file.Close()
		return false, nil, err
	}
	if err = file.Close(); err != nil {
		return false, nil, err
	}
	contexts := []string{"dump", "managed", "ignored", "status", "unmanaged", "apply"}
	for _, contextName := range contexts {
		override, marshalErr := json.Marshal(map[string]any{
			"chezmoi": map[string]any{"command": contextName},
			"dotm":    map[string]any{"policyCandidate": path, "nativeOnly": false},
		})
		if marshalErr != nil {
			return false, nil, marshalErr
		}
		rendered, runErr := policyCommand(ctx, command, "--override-data", string(override), "execute-template",
			"--file", filepath.Join(policy.SourceDir, ".chezmoiignore"))
		if runErr != nil {
			return false, nil, fmt.Errorf("render policy integration for %s: %w", contextName, runErr)
		}
		found := false
		for _, line := range strings.Split(string(rendered), "\n") {
			if line == marker {
				found = true
				break
			}
		}
		if !found {
			return false, nil, nil
		}
	}
	override, err := json.Marshal(map[string]any{
		"dotm": map[string]any{"policyCandidate": path, "nativeOnly": false},
	})
	if err != nil {
		return false, nil, err
	}
	if _, err = policyCommand(ctx, command, "--override-data", string(override), "dump", "--format=json"); err != nil {
		return false, nil, err
	}
	managed, err := policyCommand(ctx, command, "--override-data", string(override), "managed", "--nul-path-separator")
	return true, managed, err
}

func isIgnoredByAncestor(path string, ignored map[string]bool) bool {
	for {
		if ignored[path] {
			return true
		}
		separator := strings.LastIndexByte(path, '/')
		if separator < 0 {
			return ignored["."]
		}
		path = path[:separator]
	}
}
