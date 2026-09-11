package manifest

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func fixture(t *testing.T, catalog, configurations string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{"catalog.toml": catalog, "configurations.toml": configurations} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestSharedDependenciesAndDescriptors(t *testing.T) {
	dir := fixture(t, `
[root]
dependencies = ["left", "right"]
nix = [{attr = "tool", outputs = ["man", "bin"]}, "simple"]
[left]
dependencies = ["shared"]
nix = [{attr = "tool", outputs = ["bin", "man"]}, {attr = "simple"}]
[right]
dependencies = ["shared"]
nix = ["tool"]
[shared]
nix = [{attr = "tool", outputs = ["out"]}]
`, `[[configurations]]
name = "machine"
apps = ["root", "shared"]
`)
	d, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.Resolve(d.Configs["machine"].Apps)
	if err != nil {
		t.Fatal(err)
	}
	var ids, packages []string
	for _, a := range got.Applications {
		ids = append(ids, a.ID)
	}
	for _, p := range got.Packages {
		packages = append(packages, p.Key())
	}
	if want := []string{"shared", "left", "right", "root"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("application order: got %v, want %v", ids, want)
	}
	if want := []string{"simple", "tool", "tool^bin,man", "tool^out"}; !reflect.DeepEqual(packages, want) {
		t.Fatalf("descriptors: got %v, want %v", packages, want)
	}
}

func TestInvalidManifests(t *testing.T) {
	const app = "[a]\nnix = ['a']\n"
	const config = "[[configurations]]\nname = 'machine'\napps = ['a']\n"
	tests := []struct{ name, catalog, configurations string }{
		{"duplicate ID", app + app, config},
		{"missing dependency", app + "dependencies = ['missing']\n", config},
		{"duplicate dependency", app + "dependencies = ['a', 'a']\n", config},
		{"unselected cycle", app + "dependencies = ['a']\n", ""},
		{"indirect cycle", app + "dependencies = ['b']\n[b]\nnix = ['b']\ndependencies = ['a']\n", config},
		{"unknown selected app", app, "[[configurations]]\nname = 'machine'\napps = ['missing']\n"},
		{"duplicate selected app", app, "[[configurations]]\nname = 'machine'\napps = ['a', 'a']\n"},
		{"duplicate config", app, config + config},
		{"misspelled field", app + "dependencis = []\n", config},
		{"wrong application type", "a = 42\n", config},
		{"malformed TOML", "[a\n", config},
		{"missing nix", "[a]\n", config},
		{"empty nix", "[a]\nnix = []\n", config},
		{"unsafe attr", "[a]\nnix = ['a; abort']\n", config},
		{"duplicate output", "[a]\nnix = [{attr = 'a', outputs = ['out', 'out']}]\n", config},
		{"duplicate descriptor across forms", "[a]\nnix = ['a', {attr = 'a'}]\n", config},
		{"unknown descriptor field", "[a]\nnix = [{attr = 'a', output = ['out']}]\n", config},
		{"missing attr", "[a]\nnix = [{outputs = ['out']}]\n", config},
		{"wrong attr type", "[a]\nnix = [{attr = 42}]\n", config},
		{"wrong outputs type", "[a]\nnix = [{attr = 'a', outputs = 'out'}]\n", config},
		{"wrong output element type", "[a]\nnix = [{attr = 'a', outputs = ['out', 42]}]\n", config},
		{"wrong nix type", "[a]\nnix = 'a'\n", config},
		{"wrong nix element type", "[a]\nnix = ['a', 42]\n", config},
		{"empty attr", "[a]\nnix = ['']\n", config},
		{"invalid ID", "['bad id']\nnix = ['a']\n", config},
		{"legacy array catalog", "[[apps]]\nid = 'a'\npackages = [{attr = 'a'}]\n", config},
		{"legacy packages field", "[a]\npackages = ['a']\n", config},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := Load(fixture(t, tt.catalog, tt.configurations))
			if err == nil || data != nil {
				t.Fatalf("invalid manifest must be rejected without usable data: got data=%v, err=%v", data, err)
			}
		})
	}
}

func TestEmptyConfigurationAndCatalog(t *testing.T) {
	d, err := Load(fixture(t, "", "[[configurations]]\nname = 'empty'\napps = []\n"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := d.Resolve(d.Configs["empty"].Apps)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Applications) != 0 || len(result.Packages) != 0 {
		t.Fatalf("empty configuration resolved to %+v", result)
	}
}
