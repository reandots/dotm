package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolvePath expands the supported path variables and returns an absolute,
// cleaned path. It intentionally implements only a small, non-evaluating
// subset of shell parameter expansion.
func resolvePath(value, catalogDir string) (string, error) {
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
			return "", fmt.Errorf("unsupported user home expansion in hook path %q", value)
		}
		var ok bool
		home, ok = os.LookupEnv("HOME")
		if !ok || home == "" {
			return "", fmt.Errorf("HOME is unset or empty for hook path %q", value)
		}
		value = value[1:]
	}

	expanded, err := expandVariables(value)
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

func expandVariables(value string) (string, error) {
	var out strings.Builder
	out.Grow(len(value))
	for i := 0; i < len(value); {
		switch value[i] {
		case '`':
			return "", fmt.Errorf("backticks are unsupported in hook path")
		case '$':
			if i+1 >= len(value) {
				return "", fmt.Errorf("malformed variable expansion in hook path")
			}
			if value[i+1] == '(' {
				return "", fmt.Errorf("command substitution is unsupported in hook path")
			}
			if value[i+1] == '{' {
				end, name, fallback, hasFallback, err := parseBraced(value, i+2)
				if err != nil {
					return "", err
				}
				v, ok := os.LookupEnv(name)
				if hasFallback && (!ok || v == "") {
					v, err = expandVariables(fallback)
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
				return "", fmt.Errorf("unsupported variable expansion in hook path")
			}
			name := value[i+1 : j]
			v, ok := os.LookupEnv(name)
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
	return 0, "", "", false, fmt.Errorf("unclosed variable expansion in hook path")
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
