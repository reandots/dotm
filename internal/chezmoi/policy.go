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
	"dotm/internal/pathutil"
)

// PolicyInput identifies the selected dotm configuration independently of package readiness.
type PolicyInput struct {
	RepositoryID  string
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
	RepositoryID  string            `json:"repositoryID"`
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

// PolicyPath does not create state. Different effective source/config/destination bindings
// have separate files; profile switches atomically replace the same binding's selection.
func PolicyPath(policy Policy) (string, error) {
	binding, err := json.Marshal([]string{policy.ConfigFile, policy.SourceDir, policy.DestDir})
	if err != nil {
		return "", err
	}
	key := sha256.Sum256(binding)
	state, err := pathutil.Target("$XDG_STATE_HOME/dotm/files/chezmoi")
	if err != nil {
		return "", err
	}
	return filepath.Join(state, hex.EncodeToString(key[:])+".json"), nil
}

// CompilePolicy validates passive safety before evaluating trusted chezmoi templates.
// It neither fetches nor publishes, and deliberately bypasses any stale published policy.
func CompilePolicy(ctx context.Context, source string, input PolicyInput) (Policy, error) {
	var policy Policy
	if input.RepositoryID == "" || input.Configuration == "" {
		return policy, fmt.Errorf("policy requires repository ID and dotm configuration identity")
	}
	if err := input.Files.Err(); err != nil {
		return policy, err
	}
	validation, err := ValidateSource(ctx, source)
	if err != nil {
		return policy, err
	}
	if len(validation.Diagnostics) != 0 {
		return policy, fmt.Errorf("policy source validation failed: %v", validation.Diagnostics)
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
	policy.SchemaVersion = 1
	policy.RepositoryID = input.RepositoryID
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
// mutation ownership. Effective native-negation lint is a separate operation.
// Post-publication/manual mutation and power-loss rollback are not promised.
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
	command := applyCommand{source: source}
	configured, err := policyCommand(ctx, command, "execute-template",
		`{{ $d := default dict (get . "dotm") }}`+
			`{{ if default false (get $d "nativeOnly") }}{{ fail "normal dotm.nativeOnly must be false" }}{{ end }}`+
			`{{ default "" (get $d "policyFile") }}`)
	if err != nil {
		return err
	}
	if string(configured) != path {
		return fmt.Errorf("chezmoi data.dotm.policyFile must be %q (found %q)", path, configured)
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
		"policyFile": candidatePath, "nativeOnly": false,
	}})
	if err != nil {
		return err
	}
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
		return fmt.Errorf("root .chezmoiignore does not consume the bound external policy")
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
