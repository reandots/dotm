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
		s.Require().NoError(os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
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
		{"empty pre hook", app + "pre_hook = ''\n", config},
		{"legacy post hook", app + "post_hook = 'script'\n", config},
		{"nonstring config hook", app + "pre_cfg_hook = ['script']\n", config},
		{"empty config hook", app + "post_cfg_hook = ''\n", config},
		{"unknown post app hook", app + "post_app_hook = 'script'\n", config},
		{"wrong configs type", app + "configs = '/tmp/config'\n", config},
		{"wrong config target type", app + "configs = [42]\n", config},
		{"relative config target", app + "configs = ['relative']\n", config},
		{"targets without source", app + "configs = ['/tmp/config']\n", config},
		{"unknown source backend", app, config + "backend = 'other'\nrepository = 'repo'\n"},
		{"missing source repository", app, config + "backend = 'chezmoi'\n"},
		{"missing source backend", app, config + "repository = 'repo'\n"},
		{"blank source repository", app, config + "backend = 'chezmoi'\nrepository = '  '\n"},
		{"legacy nested source", app, config + "[configurations.configs]\nbackend = 'chezmoi'\nrepository = 'repo'\n"},
		{"wrong source type", app, config + "configs = 'repo'\n"},
		{"wrong repository type", app, config + "backend = 'chezmoi'\nrepository = 42\n"},
		{"wrong backend type", app, config + "backend = 42\nrepository = 'repo'\n"},
		{
			"unknown source key",
			app,
			config + "backend = 'chezmoi'\nrepository = 'r'\nextra = 1\n",
		},
		{"shell command hook table", app + "pre_hook = {command = 'echo hi'}\n", config},
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

func (s *ManifestSuite) TestPackageFreeClosureAndTargetOwnership() {
	s.T().Setenv("HOME", "/home/model-test")
	s.T().Setenv("XDG_CONFIG_HOME", "")
	d, err := Load(s.fixture(`
[root]
dependencies = ["left", "right"]
configs = ["$XDG_CONFIG_HOME/tool/", "${HOME}/.config/tool"]
pre_cfg_hook = "hooks/prepare"
post_cfg_hook = "hooks/finish"
[left]
dependencies = ["shared"]
nix = []
configs = ["~/.config/left/file with spaces"]
[right]
dependencies = ["shared"]
configs = ["~/.config/right"]
[shared]
nix = ["hello"]
`, `[[configurations]]
name = "machine"
apps = ["root", "shared"]
backend = "chezmoi"
repository = "https://example.invalid/dotfiles.git"
`))
	s.Require().NoError(err)
	config := d.Configs["machine"]
	s.Equal("chezmoi", config.Backend)
	s.Equal("https://example.invalid/dotfiles.git", config.Repository)
	resolved, err := d.Resolve(config.Apps)
	s.Require().NoError(err)
	var ids []string
	for _, app := range resolved.Applications {
		ids = append(ids, app.ID)
	}
	s.Equal([]string{"shared", "left", "right", "root"}, ids)
	s.Equal([]Package{{Attr: "hello"}}, resolved.Packages)
	s.Empty(d.Apps["root"].Packages)
	s.Empty(d.Apps["left"].Packages)
	s.Equal("hooks/prepare", d.Apps["root"].PreCfgHook)
	s.Equal("hooks/finish", d.Apps["root"].PostCfgHook)
	s.Equal([]ConfigTarget{
		{Path: "/home/model-test/.config/left/file with spaces", Applications: []string{"left"}},
		{Path: "/home/model-test/.config/right", Applications: []string{"right"}},
		{Path: "/home/model-test/.config/tool", Applications: []string{"root"}},
	}, resolved.Targets)
}

func (s *ManifestSuite) TestSourceValidationIncludesUnselectedConfigurations() {
	_, err := Load(s.fixture("[a]\nconfigs = ['/tmp/tool']\n", `
[[configurations]]
name = "empty"
apps = []
[[configurations]]
name = "unselected"
apps = ["a"]
`))
	s.Require().Error(err)
	s.Contains(err.Error(), `configuration "unselected"`)
}
