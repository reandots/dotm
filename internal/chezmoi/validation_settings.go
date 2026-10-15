package chezmoi

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/tailscale/hujson"
	"go.yaml.in/yaml/v3"
)

// decodeValidationConfig decodes static chezmoi TOML, YAML, JSON, or JSONC without
// evaluating templates or initializing chezmoi.
func decodeValidationConfig(path string, data []byte) (map[string]any, error) {
	var value map[string]any
	switch strings.ToLower(filepath.Ext(path)) {
	case ".toml":
		err := toml.Unmarshal(data, &value)
		return value, err
	case ".yaml", ".yml":
		err := yaml.Unmarshal(data, &value)
		return value, err
	case ".jsonc":
		standardized, err := hujson.Standardize(data)
		if err != nil {
			return nil, err
		}
		err = json.Unmarshal(standardized, &value)
		return value, err
	case ".json":
		err := json.Unmarshal(data, &value)
		return value, err
	default:
		return nil, fmt.Errorf("unsupported configuration format %q", filepath.Ext(path))
	}
}

// validationConfigValue mirrors case-insensitive field matching. Ambiguous
// spellings return a non-configuration value, rejected by each policy type check.
func validationConfigValue(config map[string]any, key string) (any, bool) {
	var matched any
	found := false
	for configKey, value := range config {
		if strings.EqualFold(configKey, key) {
			if found {
				return struct{}{}, true
			}
			matched, found = value, true
		}
	}
	return matched, found
}

func inspectValidationConfig(result *ValidationResult, path string, config map[string]any, source string) {
	rawSourceDir, sourceDirSet := validationConfigValue(config, "sourceDir")
	if raw, ok := rawSourceDir, sourceDirSet; ok && raw != nil {
		configured, valid := raw.(string)
		if !valid {
			result.add(path, "settings.sourceDir", "sourceDir must be a static string")
		} else if configured != "" {
			if configured == "~" {
				configured, _ = os.UserHomeDir()
			} else if strings.HasPrefix(configured, "~/") {
				if home, err := os.UserHomeDir(); err == nil {
					configured = filepath.Join(home, configured[2:])
				}
			}
			if !filepath.IsAbs(configured) {
				if absolute, err := filepath.Abs(configured); err == nil {
					configured = absolute
				}
			}
			if filepath.Clean(configured) != filepath.Clean(source) {
				result.add(path, "settings.sourceDir",
					fmt.Sprintf("sourceDir selects %q, conflicting with validated source %q", raw, source))
			}
		}
	}
	rawHooks, hooksSet := validationConfigValue(config, "hooks")
	if raw, ok := rawHooks, hooksSet; ok && raw != nil {
		hooks, valid := raw.(map[string]any)
		if !valid {
			result.add(path, "settings.hooks", "hooks must be a static mapping")
		} else if len(hooks) != 0 {
			result.add(path, "settings.hooks", "chezmoi hooks are configured; automatic hooks are forbidden")
		}
	}
	if rawGit, gitSet := validationConfigValue(config, "git"); gitSet && rawGit != nil {
		gitConfig, valid := rawGit.(map[string]any)
		if !valid {
			result.add(path, "settings.git", "git settings must be a static mapping to inspect automatic Git actions")
		} else {
			for _, key := range []string{"autoAdd", "autoCommit", "autoPush"} {
				if raw, set := validationConfigValue(gitConfig, key); set && raw != nil {
					if enabled, ok := raw.(bool); !ok {
						result.add(path, "settings.git", fmt.Sprintf("git.%s must be a static boolean", key))
					} else if enabled {
						result.add(path, "settings.git",
							fmt.Sprintf("git.%s can mutate or publish the source repository", key))
					}
				}
			}
		}
	}
}

// validateSettings inspects the normal user configuration selection. It reads
// configuration only; it does not create state, initialize chezmoi, or execute
// templates, hooks, or commands. Upstream selects custom config through --config;
// CHEZMOI_* values are script/hook environment, not config-file overrides.
func validateSettings(result *ValidationResult, source string) error {
	configPath, _, err := selectSettings(result)
	if err != nil || configPath == "" {
		return err
	}
	data, exists, err := readValidationFile(result, configPath)
	if err != nil {
		return fmt.Errorf("read chezmoi configuration %s: %w", configPath, err)
	}
	if !exists {
		result.add(configPath, "settings.parse", "selected chezmoi configuration is missing or not a regular file")
		return nil
	}
	config, err := decodeValidationConfig(configPath, data)
	if err != nil {
		result.add(configPath, "settings.parse", fmt.Sprintf("malformed chezmoi configuration: %v", err))
		return nil
	}
	inspectValidationConfig(result, configPath, config, source)
	return nil
}

// selectSettings also returns the inspected candidates so setup can pin selection.
func selectSettings(result *ValidationResult) (string, []string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", nil, fmt.Errorf("resolve home directory for chezmoi settings: %w", err)
	}
	configDirs := filepath.SplitList(os.Getenv("XDG_CONFIG_DIRS"))
	if len(configDirs) == 0 {
		configDirs = []string{"/etc/xdg"}
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	configDirs = append([]string{configHome}, configDirs...)
	var inspected []string
	for _, dir := range configDirs {
		var candidates []string
		for _, ext := range []string{"jsonc", "json", "toml", "yaml", "yml"} {
			candidate := filepath.Join(dir, "chezmoi", "chezmoi."+ext)
			inspected = append(inspected, candidate)
			// Caller-selected XDG config locations are intentionally inspected read-only.
			if _, statErr := os.Lstat(candidate); statErr == nil { //nolint:gosec // G703: no extraction or writes.
				candidates = append(candidates, candidate)
			} else if !os.IsNotExist(statErr) {
				return "", inspected, fmt.Errorf("inspect chezmoi configuration candidate %s: %w", candidate, statErr)
			}
		}
		if len(candidates) > 1 {
			result.add(filepath.Join(dir, "chezmoi"), "settings.selection",
				fmt.Sprintf("multiple chezmoi configuration files: %s", strings.Join(candidates, ", ")))
			return "", inspected, nil
		}
		if len(candidates) == 1 {
			return candidates[0], inspected, nil
		}
	}
	return "", inspected, nil
}
