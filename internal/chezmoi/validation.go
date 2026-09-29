package chezmoi

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Diagnostic describes incompatible or malformed checked input, not a dotm crash.
// Path identifies a source entry or the effective user configuration file.
// Code is a stable category; Message supplies the human-readable explanation.
type Diagnostic struct {
	Path    string
	Code    string
	Message string
}

// ValidationResult describes static policy inspection, not template evaluation.
// An empty Diagnostics slice means policy success, not that apply cannot fail.
type ValidationResult struct {
	SourceDir   string
	Diagnostics []Diagnostic
}

func (r *ValidationResult) add(path, code, message string) {
	r.Diagnostics = append(r.Diagnostics, Diagnostic{Path: path, Code: code, Message: message})
}

// ValidateSource inspects the entire used local source and normal user settings.
// It never invokes chezmoi, evaluates templates, fetches, or writes state. The
// caller must prevent concurrent mutation through subsequent application.
// Source/configuration parse and policy failures are diagnostics. Invalid dotm
// arguments, cancellation, and host I/O failures are returned as errors.
func ValidateSource(ctx context.Context, source string) (ValidationResult, error) {
	result := ValidationResult{SourceDir: filepath.Clean(source)}
	if !filepath.IsAbs(source) {
		return result, fmt.Errorf("chezmoi source must be absolute: %q", source)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := validateSettings(&result, result.SourceDir); err != nil {
		return result, err
	}
	info, err := os.Lstat(result.SourceDir)
	if errors.Is(err, fs.ErrNotExist) {
		result.add(source, "source-syntax", "source directory does not exist")
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if !info.IsDir() {
		result.add(source, "source-syntax", "source root must be a real directory")
		return result, nil
	}
	rootFile := filepath.Join(result.SourceDir, ".chezmoiroot")
	data, exists, err := readValidationFile(&result, rootFile)
	if err != nil {
		return result, err
	}
	if exists {
		root := strings.TrimSpace(string(data))
		if !validSourceRoot(root) {
			result.add(rootFile, "source-syntax", ".chezmoiroot must be a nonempty relative path without '..'")
			return result, nil
		}
		current := result.SourceDir
		for _, component := range strings.Split(root, string(filepath.Separator)) {
			current = filepath.Join(current, component)
			rootInfo, rootErr := os.Lstat(current)
			if errors.Is(rootErr, fs.ErrNotExist) {
				result.add(rootFile, "source-syntax", "effective source directory does not exist")
				return result, nil
			}
			if rootErr != nil {
				return result, rootErr
			}
			if !rootInfo.IsDir() {
				result.add(rootFile, "source-policy", "effective source root must contain only real directories")
				return result, nil
			}
		}
		result.SourceDir = current
	}
	walker := sourceValidator{ctx: ctx, result: &result, source: filepath.Clean(source)}
	err = walker.walk(result.SourceDir, sourceEntries)
	return result, err
}

func validSourceRoot(root string) bool {
	if root == "" || filepath.IsAbs(root) {
		return false
	}
	for _, component := range strings.Split(root, string(filepath.Separator)) {
		if component == ".." {
			return false
		}
	}
	return true
}

type sourceEntryMode int

const (
	sourceEntries sourceEntryMode = iota
	literalEntries
	dataEntries
	templateEntries
	scriptEntries
)

type sourceValidator struct {
	ctx    context.Context
	result *ValidationResult
	source string
}

func (v sourceValidator) walk(dir string, mode sourceEntryMode) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		v.result.add(dir, "source-syntax", "effective source directory does not exist")
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		v.result.add(dir, "source-syntax", "source directory must be a real directory")
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if dir == v.result.SourceDir && mode == sourceEntries {
		configTemplates := 0
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".tmpl") && specialFormat(entry.Name(), ".chezmoi", true) {
				configTemplates++
			}
		}
		if configTemplates > 1 {
			v.result.add(dir, "source-syntax", "multiple chezmoi configuration templates")
		}
	}
	for _, entry := range entries {
		if err := v.ctx.Err(); err != nil {
			return err
		}
		if err := v.entry(filepath.Join(dir, entry.Name()), entry, mode); err != nil {
			return err
		}
	}
	return nil
}

func (v sourceValidator) entry(path string, entry fs.DirEntry, mode sourceEntryMode) error {
	name := entry.Name()
	// Hidden ordinary entries are not chezmoi input, even under ignore rules.
	if mode != literalEntries && strings.HasPrefix(name, ".") && !strings.HasPrefix(name, ".chezmoi") {
		return nil
	}
	info, err := entry.Info()
	if err != nil {
		return err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		info, err = os.Stat(path)
		if errors.Is(err, fs.ErrNotExist) {
			v.result.add(path, "source-syntax", "dangling source symlink")
			return nil
		}
		if err != nil {
			return err
		}
		if info.IsDir() {
			v.result.add(path, "source-policy", "source directory symlinks cannot be passively inspected")
			return nil
		}
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		v.result.add(path, "source-syntax", "unsupported source entry type")
		return nil
	}
	if mode == literalEntries {
		if info.IsDir() {
			return v.walk(path, literalEntries)
		}
		return nil
	}
	if mode != sourceEntries && strings.HasPrefix(name, ".chezmoi") {
		v.result.add(path, "source-syntax", "special chezmoi entry is not allowed in this special directory")
		return nil
	}
	if mode == sourceEntries {
		handled, specialErr := v.special(path, name)
		if handled || specialErr != nil {
			return specialErr
		}
	}
	if mode == dataEntries {
		if info.IsDir() {
			return v.walk(path, mode)
		}
		return v.configData(path, false)
	}
	if mode == templateEntries {
		if info.IsDir() {
			return v.walk(path, mode)
		}
		return nil // Trusted partials are not parsed or executed during policy inspection.
	}
	if info.IsDir() {
		if mode == scriptEntries {
			return v.walk(path, mode)
		}
		target, forbidden, external := directoryPolicy(name)
		v.checkName(path, target, forbidden)
		if external {
			return v.walk(path, literalEntries)
		}
		return v.walk(path, mode)
	}
	target, forbidden := filePolicy(name)
	v.checkName(path, target, forbidden)
	if mode == scriptEntries && forbidden != "run" {
		v.result.add(path, "source-syntax", "non-script entry in .chezmoiscripts")
	}
	return nil
}

func (v sourceValidator) checkName(path, target, forbidden string) {
	if target == "" || target == "." || target == ".." {
		v.result.add(path, "source-syntax", "source attributes produce an invalid target name")
	}
	if forbidden != "" {
		v.result.add(path, "source-policy", forbidden+" actions are forbidden in passive sources")
	}
}

func (v sourceValidator) special(path, name string) (bool, error) {
	switch {
	case name == ".chezmoiremove" || name == ".chezmoiremove.tmpl":
		v.result.add(path, "source-policy", ".chezmoiremove can delete destination data")
	case name == ".chezmoiexternals" || specialFormat(name, ".chezmoiexternal", true):
		v.result.add(path, "source-policy", "automatic externals are forbidden in passive sources")
	case name == ".chezmoiscripts":
		return true, v.walk(path, scriptEntries)
	case name == ".chezmoitemplates":
		return true, v.walk(path, templateEntries)
	case name == ".chezmoidata":
		return true, v.walk(path, dataEntries)
	case specialFormat(name, ".chezmoidata", false):
		return true, v.configData(path, false)
	case strings.HasSuffix(name, ".tmpl") && specialFormat(name, ".chezmoi", true) &&
		filepath.Dir(path) == v.result.SourceDir:
		return true, v.configData(path, true)
	case strings.HasPrefix(name, ".chezmoi"):
		// Includes .chezmoiignore: never evaluate it or use it to hide policy violations.
		return true, nil
	default:
		return false, nil
	}
	return true, nil
}

func specialFormat(name, prefix string, template bool) bool {
	if template {
		name = strings.TrimSuffix(name, ".tmpl")
	}
	return name == prefix+".toml" || name == prefix+".yaml" || name == prefix+".yml" ||
		name == prefix+".json" || name == prefix+".jsonc"
}

func (v sourceValidator) configData(path string, repositoryConfig bool) error {
	data, exists, err := readValidationFile(v.result, path)
	if err != nil || !exists {
		return err
	}
	if repositoryConfig && (strings.Contains(string(data), "{{") ||
		strings.Contains(string(data), "chezmoi:template:")) {
		v.result.add(path, "source-policy",
			"dynamic repository configuration or template directives require bootstrap evaluation")
		return nil
	}
	config, err := decodeValidationConfig(strings.TrimSuffix(path, ".tmpl"), data)
	if err != nil {
		v.result.add(path, "source-syntax", err.Error())
		return nil
	}
	if repositoryConfig {
		inspectValidationConfig(v.result, path, config, v.source)
	}
	return nil
}

// readValidationFile refuses special nodes rather than opening a FIFO/device.
func readValidationFile(result *ValidationResult, path string) ([]byte, bool, error) {
	info, err := os.Stat(path) //nolint:gosec // G703: intentionally inspect caller-selected input, no writes.
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		result.add(path, "source-syntax", "expected a regular input file")
		return nil, false, nil
	}
	data, err := os.ReadFile(path) //nolint:gosec // G703: caller-selected regular input, no extraction or writes.
	return data, err == nil, err
}

// Attribute order mirrors chezmoi v2.74.1 attr.go. This intentionally does not
// search substrings or repeatedly strip arbitrary prefixes: private_run_x is a
// regular file, literal_run_x is literal, and run_literal_x is still a script.
func directoryPolicy(name string) (string, string, bool) {
	name, remove := strings.CutPrefix(name, "remove_")
	name, external := strings.CutPrefix(name, "external_")
	name, exact := strings.CutPrefix(name, "exact_")
	name = trimPrefixes(name, "private_", "readonly_")
	name = targetPrefix(name)
	if remove {
		return name, "remove", external
	}
	if exact {
		return name, "exact", external
	}
	return name, "", external
}

func filePolicy(name string) (string, string) {
	forbidden := ""
	switch {
	case strings.HasPrefix(name, "create_"):
		name = trimPrefixes(strings.TrimPrefix(name, "create_"),
			"encrypted_", "private_", "readonly_", "empty_", "executable_")
	case strings.HasPrefix(name, "remove_"):
		forbidden = "remove"
		name = strings.TrimPrefix(name, "remove_")
	case strings.HasPrefix(name, "run_"):
		forbidden = "run"
		name = strings.TrimPrefix(name, "run_")
		if after, ok := strings.CutPrefix(name, "once_"); ok {
			name = after
		} else {
			name = strings.TrimPrefix(name, "onchange_")
		}
		if after, ok := strings.CutPrefix(name, "before_"); ok {
			name = after
		} else {
			name = strings.TrimPrefix(name, "after_")
		}
	case strings.HasPrefix(name, "symlink_"):
		name = strings.TrimPrefix(name, "symlink_")
	case strings.HasPrefix(name, "modify_"):
		forbidden = "modify"
		name = trimPrefixes(strings.TrimPrefix(name, "modify_"),
			"encrypted_", "private_", "readonly_", "executable_")
	default:
		name = trimPrefixes(name, "encrypted_", "private_", "readonly_", "empty_", "executable_")
	}
	name = targetPrefix(name)
	if after, ok := strings.CutSuffix(name, ".literal"); ok {
		name = after
	} else {
		name = strings.TrimSuffix(name, ".tmpl")
		name = strings.TrimSuffix(name, ".literal")
	}
	return name, forbidden
}

func trimPrefixes(name string, prefixes ...string) string {
	for _, prefix := range prefixes {
		name = strings.TrimPrefix(name, prefix)
	}
	return name
}

func targetPrefix(name string) string {
	if after, ok := strings.CutPrefix(name, "dot_"); ok {
		return "." + after
	}
	return strings.TrimPrefix(name, "literal_")
}
