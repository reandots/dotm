// Package manifest loads and validates dotm's repository data and local selection.
package manifest

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"dotm/internal/pathutil"

	"github.com/pelletier/go-toml/v2"
)

type Package struct {
	Attr    string   `toml:"attr"`
	Outputs []string `toml:"outputs"`
}

type Application struct {
	ID           string
	Dependencies []string
	Packages     []Package
	PreHook      string
	Configs      []string
	Unmanaged    []string
	PreCfgHook   string
	PostCfgHook  string
}

type Configuration struct {
	Name       string   `toml:"name"`
	Apps       []string `toml:"apps"`
	Backend    string   `toml:"backend,omitempty"`
	Repository string   `toml:"repository,omitempty"`
}

type ConfigurationSource struct {
	Backend    string `toml:"backend"`
	Repository string `toml:"repository"`
}

type Data struct {
	Apps             map[string]Application
	Configs          map[string]Configuration
	CatalogDir       string
	FileDeclarations []FileDeclaration
	FileDiagnostics  []FileDiagnostic
}

var (
	identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)
	attribute  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_'-]*(\.[a-zA-Z_][a-zA-Z0-9_'-]*)*$`)
)

func decodeContents(path string, content []byte, target any, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := toml.NewDecoder(bytes.NewReader(content)).DisallowUnknownFields().Decode(target); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// Load validates the entire catalog and every configuration, including unselected ones.
func Load(dir string) (*Data, error) {
	return load(dir, os.ReadFile)
}

// LoadContents validates planned manifests without creating files or changing their path context.
func LoadContents(dir string, catalog, configurations []byte) (*Data, error) {
	return load(dir, func(path string) ([]byte, error) {
		if filepath.Base(path) == "catalog.toml" {
			return catalog, nil
		}
		return configurations, nil
	})
}

func load(dir string, readFile func(string) ([]byte, error)) (*Data, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve catalog directory: %w", err)
	}
	var catalog map[string]struct {
		Dependencies []string `toml:"dependencies"`
		Nix          []any    `toml:"nix"`
		Configs      []string `toml:"configs"`
		Unmanaged    []string `toml:"unmanaged"`
		PreHook      *string  `toml:"pre_hook"`
		PreCfgHook   *string  `toml:"pre_cfg_hook"`
		PostCfgHook  *string  `toml:"post_cfg_hook"`
	}
	catalogPath := filepath.Join(dir, "catalog.toml")
	catalogContent, readErr := readFile(catalogPath)
	if err := decodeContents(catalogPath, catalogContent, &catalog, readErr); err != nil {
		return nil, err
	}
	d := &Data{Apps: make(map[string]Application), Configs: make(map[string]Configuration), CatalogDir: dir}
	ids := make([]string, 0, len(catalog))
	for id := range catalog {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if !identifier.MatchString(id) {
			return nil, fmt.Errorf("%s: invalid application ID %q", catalogPath, id)
		}
		raw := catalog[id]
		app := Application{ID: id, Dependencies: raw.Dependencies, Packages: make([]Package, 0, len(raw.Nix))}
		for _, hook := range []struct {
			name   string
			value  *string
			target *string
		}{
			{"pre_hook", raw.PreHook, &app.PreHook},
			{"pre_cfg_hook", raw.PreCfgHook, &app.PreCfgHook},
			{"post_cfg_hook", raw.PostCfgHook, &app.PostCfgHook},
		} {
			if hook.value != nil {
				if *hook.value == "" {
					return nil, fmt.Errorf(
						"%s: application %q: %s must be a nonempty file path",
						catalogPath, id, hook.name,
					)
				}
				*hook.target = *hook.value
			}
		}
		for _, field := range []struct {
			name   string
			values []string
			target *[]string
		}{
			{"configs", raw.Configs, &app.Configs},
			{"unmanaged", raw.Unmanaged, &app.Unmanaged},
		} {
			seen := make(map[string]bool, len(field.values))
			for i, value := range field.values {
				target, targetErr := pathutil.Target(value)
				if targetErr != nil {
					return nil, fmt.Errorf("%s: application %q: %s[%d] = %q: %w",
						catalogPath, id, field.name, i, value, targetErr)
				}
				d.FileDeclarations = append(d.FileDeclarations, FileDeclaration{
					Source: catalogPath, Application: id, Field: field.name, Index: i, Input: value, Path: target,
				})
				if !seen[target] {
					seen[target] = true
					*field.target = append(*field.target, target)
				}
			}
		}
		packages := make(map[string]bool)
		for i, value := range raw.Nix {
			p, err := parsePackage(value)
			if err != nil {
				return nil, fmt.Errorf("%s: application %q: nix[%d]: %w", catalogPath, id, i, err)
			}
			if !attribute.MatchString(p.Attr) {
				return nil, fmt.Errorf("%s: application %q: invalid package attribute %q", catalogPath, app.ID, p.Attr)
			}
			outputs := make(map[string]bool)
			for _, output := range p.Outputs {
				if !identifier.MatchString(output) || outputs[output] {
					return nil, fmt.Errorf(
						"%s: application %q: invalid or duplicate output %q",
						catalogPath, app.ID, output,
					)
				}
				outputs[output] = true
			}
			sort.Strings(p.Outputs)
			key := p.Key()
			if packages[key] {
				return nil, fmt.Errorf("%s: application %q: duplicate package %s", catalogPath, app.ID, key)
			}
			packages[key] = true
			app.Packages = append(app.Packages, p)
		}
		d.Apps[app.ID] = app
	}
	files := FileModel{Declarations: d.FileDeclarations}
	files.lint()
	if err := files.Err(); err != nil {
		return nil, err
	}
	d.FileDiagnostics = files.Diagnostics
	for _, id := range d.ApplicationIDs() {
		if err := checkReferences("application "+id, d.Apps[id].Dependencies, d.Apps); err != nil {
			return nil, fmt.Errorf("%s: %w", catalogPath, err)
		}
	}
	// Visit all applications so unused cycles cannot silently survive validation.
	if _, err := d.Resolve(d.ApplicationIDs()); err != nil {
		return nil, fmt.Errorf("%s: %w", catalogPath, err)
	}
	configPath := filepath.Join(dir, "configurations.toml")
	var configurations struct {
		Configurations []Configuration `toml:"configurations"`
	}
	configurationContent, readErr := readFile(configPath)
	if err := decodeContents(configPath, configurationContent, &configurations, readErr); err != nil {
		return nil, err
	}
	for _, config := range configurations.Configurations {
		if !identifier.MatchString(config.Name) {
			return nil, fmt.Errorf("%s: invalid configuration name %q", configPath, config.Name)
		}
		if _, exists := d.Configs[config.Name]; exists {
			return nil, fmt.Errorf("%s: duplicate configuration %q", configPath, config.Name)
		}
		if err := checkReferences("configuration "+config.Name, config.Apps, d.Apps); err != nil {
			return nil, fmt.Errorf("%s: %w", configPath, err)
		}
		if config.Backend != "" || config.Repository != "" {
			if config.Backend != "chezmoi" || strings.TrimSpace(config.Repository) == "" {
				return nil, fmt.Errorf("%s: configuration %q: source requires backend 'chezmoi' and a repository",
					configPath, config.Name)
			}
		}
		resolved, err := d.Resolve(config.Apps)
		if err != nil {
			return nil, fmt.Errorf("%s: configuration %q: %w", configPath, config.Name, err)
		}
		if len(resolved.Targets) > 0 && config.Backend == "" {
			return nil, fmt.Errorf("%s: configuration %q: config targets require backend and repository",
				configPath, config.Name)
		}
		d.Configs[config.Name] = config
	}
	return d, nil
}

// parsePackage normalizes string shorthand and strictly checks expanded descriptors.
func parsePackage(value any) (Package, error) {
	if attr, ok := value.(string); ok {
		return Package{Attr: attr}, nil
	}
	table, ok := value.(map[string]any)
	if !ok {
		return Package{}, fmt.Errorf("expected a package attribute string or a table with attr and optional outputs")
	}
	for key := range table {
		if key != "attr" && key != "outputs" {
			return Package{}, fmt.Errorf("unknown package field %q", key)
		}
	}
	attr, ok := table["attr"].(string)
	if !ok {
		return Package{}, fmt.Errorf("attr must be a string")
	}
	p := Package{Attr: attr}
	if value, exists := table["outputs"]; exists {
		outputs, ok := value.([]any)
		if !ok {
			return Package{}, fmt.Errorf("outputs must be an array of strings")
		}
		p.Outputs = make([]string, 0, len(outputs))
		for _, value := range outputs {
			output, ok := value.(string)
			if !ok {
				return Package{}, fmt.Errorf("outputs must contain only strings")
			}
			p.Outputs = append(p.Outputs, output)
		}
	}
	return p, nil
}

func checkReferences(owner string, refs []string, apps map[string]Application) error {
	seen := make(map[string]bool)
	for _, id := range refs {
		if _, exists := apps[id]; !exists {
			return fmt.Errorf("%s: unknown application %q", owner, id)
		}
		if seen[id] {
			return fmt.Errorf("%s: duplicate application reference %q", owner, id)
		}
		seen[id] = true
	}
	return nil
}

func (d *Data) ApplicationIDs() []string {
	ids := make([]string, 0, len(d.Apps))
	for id := range d.Apps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// SelectionPath follows XDG conventions; relative XDG_CONFIG_HOME is invalid.
func SelectionPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	if !filepath.IsAbs(base) {
		return "", fmt.Errorf("configuration directory must be absolute: %q", base)
	}
	return filepath.Join(base, "dotm", "config.toml"), nil
}

func (d *Data) Selection(path string) (Configuration, error) {
	content, readErr := os.ReadFile(path)
	if readErr != nil {
		return Configuration{}, fmt.Errorf("%s: %w", path, readErr)
	}
	name, err := SelectionContents(path, content)
	if err != nil {
		return Configuration{}, err
	}
	config, ok := d.Configs[name]
	if !ok {
		return Configuration{}, fmt.Errorf("%s: unknown configuration %q", path, name)
	}
	return config, nil
}

// SelectionContents validates machine-local selection bytes without reading a file.
func SelectionContents(path string, content []byte) (string, error) {
	var selection struct {
		Configuration string `toml:"configuration"`
	}
	if err := decodeContents(path, content, &selection, nil); err != nil {
		return "", err
	}
	if !identifier.MatchString(selection.Configuration) {
		return "", fmt.Errorf("%s: configuration requires a valid configuration name", path)
	}
	return selection.Configuration, nil
}

// Key identifies a descriptor independently of the order of its selected outputs.
// An absent output list means Nix's default output selection, not explicit "out".
func (p Package) Key() string {
	if len(p.Outputs) == 0 {
		return p.Attr
	}
	return p.Attr + "^" + strings.Join(p.Outputs, ",")
}
