package chezmoi

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"dotm/internal/manifest"
)

func inspectEffectiveRules(ctx context.Context, source string, policy Policy) ([]manifest.FileDiagnostic, error) {
	entries, err := effectiveSourceEntries(policy.SourceDir)
	if err != nil {
		return nil, fmt.Errorf("inspect effective source: %w", err)
	}
	var ignoreFiles []string
	for _, path := range entries {
		if filepath.Base(path) == ".chezmoiignore" {
			ignoreFiles = append(ignoreFiles, path)
		}
	}
	command := applyCommand{source: source}
	contexts := []string{"dump", "managed", "ignored", "status", "unmanaged", "apply"}
	var diagnostics []manifest.FileDiagnostic
	for _, ignoreFile := range ignoreFiles {
		scope, scopeErr := policyCommand(ctx, command, "--override-data", `{"dotm":{"nativeOnly":true}}`,
			"target-path", filepath.Dir(ignoreFile))
		if scopeErr != nil {
			return nil, fmt.Errorf("map native ignore scope %q: %w", ignoreFile, scopeErr)
		}
		absoluteScope := strings.TrimSuffix(string(scope), "\n")
		scopeRelative, relErr := filepath.Rel(policy.DestDir, absoluteScope)
		if relErr != nil {
			return nil, fmt.Errorf("map native ignore scope %q: %w", ignoreFile, relErr)
		}
		scopePath := filepath.ToSlash(scopeRelative)
		for _, contextName := range contexts {
			override, marshalErr := jsonMarshalCommandContext(contextName)
			if marshalErr != nil {
				return nil, marshalErr
			}
			rendered, runErr := policyCommand(ctx, command, "--override-data", override,
				"execute-template", "--file", ignoreFile)
			if runErr != nil {
				return nil, fmt.Errorf("render native ignore file %q for %s: %w", ignoreFile, contextName, runErr)
			}
			for lineNumber, line := range strings.Split(string(rendered), "\n") {
				trimmed := trimIgnoreComment(line)
				if trimmed == "" {
					continue
				}
				negated := strings.HasPrefix(trimmed, "!")
				pattern := trimmed
				if negated {
					pattern = strings.TrimSpace(strings.TrimPrefix(trimmed, "!"))
				}
				if pattern == "" {
					return nil, fmt.Errorf("empty native ignore rule in %s:%d", ignoreFile, lineNumber+1)
				}
				mapped := filepath.ToSlash(filepath.Join(scopePath, filepath.FromSlash(pattern)))
				if !negated {
					_, validationErr := nativeNegationIntersects(mapped, ".dotm-rule-validation")
					if validationErr != nil {
						return nil, fmt.Errorf("invalid native ignore rule %q in %s:%d: %w",
							pattern, ignoreFile, lineNumber+1, validationErr)
					}
					continue
				}
				for _, exclusion := range policy.Exclusions {
					root, rootErr := filepath.Rel(policy.DestDir, exclusion.Root)
					if rootErr != nil {
						return nil, rootErr
					}
					intersects, matchErr := nativeNegationIntersects(mapped, filepath.ToSlash(root))
					if matchErr != nil {
						return nil, fmt.Errorf("invalid native ignore rule %q in %s:%d: %w",
							pattern, ignoreFile, lineNumber+1, matchErr)
					}
					if intersects {
						diagnostics = append(diagnostics, manifest.FileDiagnostic{
							Severity: manifest.FileError, Declaration: exclusion.Origin,
							Message: fmt.Sprintf("native ignore negation %q at %s:%d (context %s) "+
								"may override generated exclusion %q",
								pattern, ignoreFile, lineNumber+1, contextName, exclusion.Root),
						})
					}
				}
			}
		}
	}
	return diagnostics, nil
}

func literalRuleDirectory(name string) bool {
	for _, prefix := range []string{"remove_", "external_", "exact_", "private_", "readonly_"} {
		name = strings.TrimPrefix(name, prefix)
	}
	return strings.HasPrefix(name, "literal_")
}

func trimIgnoreComment(line string) string {
	for offset, r := range line {
		if r != '#' {
			continue
		}
		if offset == 0 {
			return strings.TrimSpace(line[:offset])
		}
		previous, _ := utf8.DecodeLastRuneInString(line[:offset])
		if unicode.IsSpace(previous) {
			return strings.TrimSpace(line[:offset])
		}
	}
	return strings.TrimSpace(line)
}

// jsonMarshalCommandContext is kept local so the rendered context is explicit.
func jsonMarshalCommandContext(command string) (string, error) {
	data, err := json.Marshal(map[string]any{
		"chezmoi": map[string]any{"command": command},
		"dotm":    map[string]any{"nativeOnly": true},
	})
	return string(data), err
}

func effectiveSourceEntries(sourceRoot string) ([]string, error) {
	var entries []string
	root := filepath.Clean(sourceRoot)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		literal := insideLiteralRuleDirectory(root, path)
		if entry.IsDir() {
			if filepath.Clean(path) == root {
				return nil
			}
			name := entry.Name()
			_, _, external := directoryPolicy(name)
			if !literal && (strings.HasPrefix(name, ".") || external) {
				return fs.SkipDir
			}
		} else if !literal && strings.HasPrefix(entry.Name(), ".") && entry.Name() != ".chezmoiignore" {
			return nil
		}
		entries = append(entries, path)
		return nil
	})
	return entries, err
}

func insideLiteralRuleDirectory(root, path string) bool {
	relative, err := filepath.Rel(root, filepath.Dir(path))
	if err != nil {
		return false
	}
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if literalRuleDirectory(component) {
			return true
		}
	}
	return false
}
