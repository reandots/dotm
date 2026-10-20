package pathutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Hook expands a hook path using the supported non-evaluating variable syntax.
// Relative paths are resolved against catalogDir.
func Hook(value, catalogDir string) (string, error) {
	if strings.IndexByte(value, 0) >= 0 {
		return "", fmt.Errorf("hook path contains NUL")
	}
	if value == "" {
		return "", fmt.Errorf("hook path is empty")
	}
	var home string
	tilde := value[0] == '~'
	if tilde {
		if value != "~" && !strings.HasPrefix(value, "~/") {
			return "", fmt.Errorf("hook path must use ~ or ~/ for home expansion")
		}
		var ok bool
		home, ok = os.LookupEnv("HOME")
		if !ok || home == "" {
			return "", fmt.Errorf("HOME is unset or empty for hook path %q", value)
		}
		value = value[1:]
	}
	expanded, err := expandVariables(value, false)
	if err != nil {
		return "", err
	}
	if tilde {
		expanded = home + expanded
	}
	if expanded == "" {
		return "", fmt.Errorf("hook path expands to empty")
	}
	if strings.IndexByte(expanded, 0) >= 0 {
		return "", fmt.Errorf("expanded hook path contains NUL")
	}
	if !filepath.IsAbs(expanded) {
		expanded = filepath.Join(catalogDir, expanded)
	}
	absolute, err := filepath.Abs(expanded)
	if err != nil {
		return "", fmt.Errorf("make hook path absolute: %w", err)
	}
	return filepath.Clean(absolute), nil
}

// Target expands a configuration destination and returns its cleaned absolute path.
func Target(value string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("target path is empty")
	}
	if strings.IndexByte(value, 0) >= 0 {
		return "", fmt.Errorf("target path contains NUL")
	}
	if strings.HasPrefix(value, "~") && value != "~" && !strings.HasPrefix(value, "~/") {
		return "", fmt.Errorf("target path must use ~ or ~/ for home expansion")
	}
	expanded, err := expandVariables(value, true)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(value, "~") {
		home, ok := os.LookupEnv("HOME")
		if !ok || home == "" {
			return "", fmt.Errorf("HOME is unset or empty for target path %q", value)
		}
		expanded = home + expanded[1:]
	}
	if expanded == "" || strings.IndexByte(expanded, 0) >= 0 {
		return "", fmt.Errorf("target path expands to empty or contains NUL")
	}
	if !filepath.IsAbs(expanded) {
		return "", fmt.Errorf("target path must be absolute")
	}
	return filepath.Clean(expanded), nil
}

func expandVariables(value string, target bool) (string, error) {
	var out strings.Builder
	out.Grow(len(value))
	for i := 0; i < len(value); {
		switch value[i] {
		case '`':
			return "", fmt.Errorf("backticks are unsupported in path")
		case '$':
			if i+1 >= len(value) {
				return "", fmt.Errorf("malformed variable expansion in path")
			}
			if value[i+1] == '(' {
				return "", fmt.Errorf("command substitution is unsupported in path")
			}
			if value[i+1] == '{' {
				end, name, fallback, hasFallback, err := parseBraced(value, i+2)
				if err != nil {
					return "", err
				}
				// Explicit shell-style defaults take precedence over implicit XDG defaults.
				v, ok := lookup(name, target && !hasFallback)
				if hasFallback && (!ok || v == "") {
					v, err = expandVariables(fallback, target)
					if err != nil {
						return "", err
					}
				} else if !ok {
					return "", fmt.Errorf("environment variable %s is unset", name)
				}
				out.WriteString(v)
				i = end + 1
				continue
			}
			j := i + 1
			for j < len(value) && isVarChar(value[j], j == i+1) {
				j++
			}
			if j == i+1 {
				return "", fmt.Errorf("unsupported variable expansion in path")
			}
			name := value[i+1 : j]
			v, ok := lookup(name, target)
			if !ok {
				return "", fmt.Errorf("environment variable %s is unset", name)
			}
			out.WriteString(v)
			i = j
		default:
			out.WriteByte(value[i])
			i++
		}
	}
	return out.String(), nil
}

func lookup(name string, target bool) (string, bool) {
	if target {
		var suffix string
		switch name {
		case "XDG_CONFIG_HOME":
			suffix = ".config"
		case "XDG_DATA_HOME":
			suffix = ".local/share"
		case "XDG_STATE_HOME":
			suffix = ".local/state"
		case "XDG_CACHE_HOME":
			suffix = ".cache"
		}
		if suffix != "" {
			if v, exists := os.LookupEnv(name); exists && v != "" {
				return v, true
			}
			home, exists := os.LookupEnv("HOME")
			if !exists || home == "" {
				return "", false
			}
			return filepath.Join(home, suffix), true
		}
	}
	v, ok := os.LookupEnv(name)
	return v, ok
}

func parseBraced(value string, start int) (end int, name, fallback string, hasFallback bool, err error) {
	depth := 0
	for i := start; i < len(value); i++ {
		if value[i] == '$' && i+1 < len(value) && value[i+1] == '{' {
			depth++
			i++
			continue
		}
		if value[i] != '}' {
			continue
		}
		if depth > 0 {
			depth--
			continue
		}
		body := value[start:i]
		sep := strings.Index(body, ":-")
		if sep >= 0 {
			name, fallback, hasFallback = body[:sep], body[sep+2:], true
		} else {
			name = body
		}
		if !validName(name) {
			return 0, "", "", false, fmt.Errorf("malformed or unsupported variable expansion ${%s}", body)
		}
		return i, name, fallback, hasFallback, nil
	}
	return 0, "", "", false, fmt.Errorf("unclosed variable expansion in path")
}

func validName(name string) bool {
	if name == "" || !isVarChar(name[0], true) {
		return false
	}
	for i := 1; i < len(name); i++ {
		if !isVarChar(name[i], false) {
			return false
		}
	}
	return true
}

func isVarChar(c byte, first bool) bool {
	return c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || (!first && c >= '0' && c <= '9')
}
