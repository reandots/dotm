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
}

type Configuration struct {
	Name string   `toml:"name"`
	Apps []string `toml:"apps"`
}

type Data struct {
	Apps    map[string]Application
	Configs map[string]Configuration
}

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)
var attribute = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_'-]*(\.[a-zA-Z_][a-zA-Z0-9_'-]*)*$`)

func decode(path string, target any) error {
	content, err := os.ReadFile(path)
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
	var catalog map[string]struct {
		Dependencies []string `toml:"dependencies"`
		Nix          []any    `toml:"nix"`
	}
	catalogPath := filepath.Join(dir, "catalog.toml")
	if err := decode(catalogPath, &catalog); err != nil {
		return nil, err
	}
	d := &Data{Apps: make(map[string]Application), Configs: make(map[string]Configuration)}
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
		if len(raw.Nix) == 0 {
			return nil, fmt.Errorf("%s: application %q needs at least one nix package", catalogPath, id)
		}
		app := Application{ID: id, Dependencies: raw.Dependencies, Packages: make([]Package, 0, len(raw.Nix))}
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
					return nil, fmt.Errorf("%s: application %q: invalid or duplicate output %q", catalogPath, app.ID, output)
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
	if err := decode(configPath, &configurations); err != nil {
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
	var selection struct {
		Configuration string `toml:"configuration"`
	}
	if err := decode(path, &selection); err != nil {
		return Configuration{}, err
	}
	if selection.Configuration == "" {
		return Configuration{}, fmt.Errorf("%s: configuration is required", path)
	}
	config, ok := d.Configs[selection.Configuration]
	if !ok {
		return Configuration{}, fmt.Errorf("%s: unknown configuration %q", path, selection.Configuration)
	}
	return config, nil
}

// Key identifies a descriptor independently of the order of its selected outputs.
// An absent output list means Nix's default output selection, not explicit "out".
func (p Package) Key() string {
	if len(p.Outputs) == 0 {
		return p.Attr
	}
	return p.Attr + "^" + strings.Join(p.Outputs, ",")
}
