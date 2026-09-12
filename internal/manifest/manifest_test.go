package manifest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"
)

type ManifestSuite struct {
	suite.Suite
}

func TestManifestSuite(t *testing.T) {
	suite.Run(t, new(ManifestSuite))
}

func (s *ManifestSuite) fixture(catalog, configurations string) string {
	s.T().Helper()
	dir := s.T().TempDir()
	for name, content := range map[string]string{"catalog.toml": catalog, "configurations.toml": configurations} {
		s.Require().NoError(os.WriteFile(filepath.Join(dir, name), []byte(content), 0600))
	}
	return dir
}

func (s *ManifestSuite) TestSharedDependenciesAndDescriptors() {
	dir := s.fixture(`
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
	s.Require().NoError(err)
	got, err := d.Resolve(d.Configs["machine"].Apps)
	s.Require().NoError(err)
	var ids, packages []string
	for _, a := range got.Applications {
		ids = append(ids, a.ID)
	}
	for _, p := range got.Packages {
		packages = append(packages, p.Key())
	}
	s.Equal([]string{"shared", "left", "right", "root"}, ids, "dependency-first application order")
	s.Equal([]string{"simple", "tool", "tool^bin,man", "tool^out"}, packages, "deduplicated canonical descriptors")
}

func (s *ManifestSuite) TestInvalidManifests() {
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
		s.Run(tt.name, func() {
			data, err := Load(s.fixture(tt.catalog, tt.configurations))
			s.Error(err, "invalid manifest must be rejected")
			s.Nil(data, "invalid manifest must not return usable data")
		})
	}
}

func (s *ManifestSuite) TestEmptyConfigurationAndCatalog() {
	d, err := Load(s.fixture("", "[[configurations]]\nname = 'empty'\napps = []\n"))
	s.Require().NoError(err)
	result, err := d.Resolve(d.Configs["empty"].Apps)
	s.Require().NoError(err)
	s.Empty(result.Applications)
	s.Empty(result.Packages)
}
