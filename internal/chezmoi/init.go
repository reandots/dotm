package chezmoi

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dotm/internal/pathutil"
)

//go:embed init_assets/portable.ignore
var portableIntegration string

//go:embed init_assets/legacy.ignore
var legacyIntegration string

type IntegrationEffect struct {
	Path, Action string
}

type IntegrationResult struct {
	Effects []IntegrationEffect
}

// SetupSource obtains an absent checkout, validates a matching one locally, or uses
// the ordinary strict replacement protocol for a different repository.
func SetupSource(ctx context.Context, repository string, confirm ConfirmReplacement) (SourceResult, error) {
	path, err := SourcePath()
	if err != nil {
		return SourceResult{}, err
	}
	manager := sourceManager{path: path, rename: os.Rename}
	if strings.TrimSpace(repository) == "" || strings.ContainsAny(repository, "\x00\r\n") {
		return SourceResult{Path: path}, fmt.Errorf("invalid configuration source repository")
	}
	if err = ctx.Err(); err != nil {
		return SourceResult{Path: path}, err
	}
	if _, markerErr := os.Lstat(manager.handoffPath()); !errors.Is(markerErr, os.ErrNotExist) {
		return SourceResult{Path: path}, fmt.Errorf("cannot continue: inspect source handoff at %s: %w",
			manager.handoffPath(), handoffError(markerErr))
	}
	if _, statErr := os.Lstat(path); statErr == nil {
		state, inspectErr := inspectCheckoutMode(ctx, path, false)
		if inspectErr != nil {
			return SourceResult{Path: path}, inspectErr
		}
		if repositoryIdentity(state.repository) == repositoryIdentity(repository) {
			return SourceResult{Path: path}, nil
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return SourceResult{Path: path}, statErr
	}
	return manager.sync(ctx, repository, confirm)
}

// PrepareIntegration creates minimal static settings when no normal configuration
// is selected, then installs the owned portable ignore block at effective root.
func PrepareIntegration(ctx context.Context, source string) (result IntegrationResult, err error) {
	plannedCheckout, err := inspectCheckoutMode(ctx, source, false)
	if err != nil {
		return result, err
	}
	validation, err := ValidateSource(ctx, source)
	if err != nil {
		return result, err
	}
	if err = validation.Err(); err != nil {
		return result, err
	}
	selected, candidates, err := selectSettings(&validation)
	if err != nil {
		return result, err
	}
	if err = validation.Err(); err != nil {
		return result, err
	}
	settingsPath, err := pathutil.Target("$XDG_CONFIG_HOME/chezmoi/chezmoi.toml")
	if err != nil {
		return result, err
	}
	paths := append(candidates, filepath.Join(source, ".chezmoiroot"),
		filepath.Join(validation.SourceDir, ".chezmoiignore"))
	inputs := make([]integrationFile, 0, len(paths))
	for _, path := range paths {
		input, inspectErr := inspectIntegrationFile(path)
		if inspectErr != nil {
			return result, inspectErr
		}
		inputs = append(inputs, input)
	}
	ignore := inputs[len(inputs)-1]
	portable, _ := integrationTemplates()
	block := ownedIntegration(portable)
	updated, changed, err := integrateIgnore(ignore.data, block, ignore.path)
	if err != nil {
		return result, err
	}
	recheck := func() error {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		currentCheckout, checkoutErr := inspectCheckoutMode(ctx, source, false)
		if checkoutErr != nil {
			return checkoutErr
		}
		if !os.SameFile(plannedCheckout.info, currentCheckout.info) ||
			plannedCheckout.fingerprint != currentCheckout.fingerprint {
			return fmt.Errorf("source checkout changed since planning; rerun init")
		}
		for _, input := range inputs {
			if checkErr := input.recheck(); checkErr != nil {
				return checkErr
			}
		}
		current, validateErr := ValidateSource(ctx, source)
		if validateErr != nil {
			return validateErr
		}
		if current.SourceDir != validation.SourceDir {
			return fmt.Errorf("effective source root changed since planning; rerun init")
		}
		return current.Err()
	}
	if selected == "" {
		if err = recheck(); err != nil {
			return result, err
		}
		settings, inspectErr := inspectIntegrationFile(settingsPath)
		if inspectErr != nil {
			return result, inspectErr
		}
		if settings.info != nil {
			return result, fmt.Errorf("chezmoi settings appeared since planning; rerun init")
		}
		if err = settings.publish(ctx, []byte("mode = \"file\"\n")); err != nil {
			return result, err
		}
		result.Effects = append(result.Effects, IntegrationEffect{settingsPath, "created static chezmoi settings"})
		// The one publication is now part of our assumptions, not a stale input.
		for i := range inputs {
			if inputs[i].path == settingsPath {
				inputs[i], err = inspectIntegrationFile(settingsPath)
				if err != nil {
					return result, err
				}
			}
		}
	}
	if changed {
		if err = recheck(); err != nil {
			return result, err
		}
		if err = ignore.publish(ctx, updated); err != nil {
			return result, err
		}
		action := "updated owned policy integration"
		if ignore.info == nil {
			action = "created owned policy integration"
		}
		result.Effects = append(result.Effects, IntegrationEffect{ignore.path, action})
	}
	return result, nil
}

func integrateIgnore(data []byte, block, path string) ([]byte, bool, error) {
	text := string(data)
	const begin, end = "{{/* dotm:policy begin */}}", "{{/* dotm:policy end */}}"
	starts, ends := strings.Count(text, begin), strings.Count(text, end)
	if starts > 1 || ends > 1 || starts != ends || strings.Count(text, "dotm:policy") != starts+ends {
		return nil, false, fmt.Errorf("%s has duplicate or partial dotm policy markers; repair them manually", path)
	}
	portable, legacy := integrationTemplates()
	if starts == 1 {
		a, b := strings.Index(text, begin), strings.Index(text, end)
		if b < a {
			return nil, false, fmt.Errorf("%s has reversed dotm policy markers; repair them manually", path)
		}
		b += len(end)
		old := text[a:b]
		replacement := strings.TrimSuffix(block, "\n")
		if unrecognizedIntegration(text[:a] + text[b:]) {
			return nil, false, fmt.Errorf("%s has an additional unmarked dotm integration; correct it manually", path)
		}
		if old == replacement {
			return data, false, nil
		}
		if old != strings.TrimSuffix(ownedIntegration(legacy), "\n") {
			return nil, false, fmt.Errorf("%s has a modified dotm policy block; repair it manually", path)
		}
		return []byte(text[:a] + replacement + text[b:]), true, nil
	}
	legacyCount, portableCount := strings.Count(text, legacy), strings.Count(text, portable)
	if legacyCount+portableCount > 1 {
		return nil, false, fmt.Errorf("%s has ambiguous unmarked dotm integrations; repair manually", path)
	}
	if legacyCount == 1 {
		text = strings.Replace(text, legacy, "", 1)
	} else if portableCount == 1 {
		text = strings.Replace(text, portable, "", 1)
	}
	if unrecognizedIntegration(text) {
		return nil, false, fmt.Errorf("%s has an unrecognized or modified dotm integration; correct it manually", path)
	}
	return []byte(block + text), true, nil
}

func integrationTemplates() (portable, legacy string) {
	const userComment = "\n# Existing native rules continue here.\n"
	return strings.TrimSuffix(portableIntegration, userComment), strings.TrimSuffix(legacyIntegration, userComment)
}

func ownedIntegration(template string) string {
	return "{{/* dotm:policy begin */}}\n" + template + "{{/* dotm:policy end */}}\n"
}

// These signatures only refuse unknown integrations; they never authorize migration.
func unrecognizedIntegration(text string) bool {
	for {
		_, rest, found := strings.Cut(text, "{{")
		if !found {
			return false
		}
		action, remaining, _ := strings.Cut(rest, "}}")
		text = remaining
		if strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(action, "-")), "/*") {
			continue
		}
		for _, signature := range []string{
			"policyCandidate", "policyFile", "dotm/chezmoi-policy.json", ".dotm.policy", `get . "dotm"`,
		} {
			if strings.Contains(action, signature) {
				return true
			}
		}
	}
}

// VerifyDiscovery checks ordinary chezmoi source/config selection and external policy visibility.
func VerifyDiscovery(ctx context.Context, source string, policy Policy) error {
	command := applyCommand{}
	metadata, err := policyCommand(ctx, command, "execute-template",
		`{{ dict "sourceDir" .chezmoi.sourceDir "destDir" .chezmoi.destDir `+
			`"configFile" .chezmoi.configFile | toJson }}`)
	if err != nil {
		return err
	}
	var binding Policy
	if err = json.Unmarshal(metadata, &binding); err != nil {
		return fmt.Errorf("decode normal chezmoi discovery: %w", err)
	}
	if source != policy.Source || binding.SourceDir != policy.SourceDir ||
		binding.DestDir != policy.DestDir || binding.ConfigFile != policy.ConfigFile {
		return fmt.Errorf("normal chezmoi discovery does not match the initialized source/settings binding")
	}
	consumed, _, err := verifyPolicyConsumption(ctx, command, policy)
	if err != nil {
		return err
	}
	if !consumed {
		return fmt.Errorf("normal chezmoi discovery does not consume initialized policy, including empty exclusions")
	}
	_, err = policyCommand(ctx, command, "dump", "--format=json")
	return err
}
