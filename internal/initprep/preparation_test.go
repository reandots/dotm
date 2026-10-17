package initprep

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"dotm/internal/manifest"
	"github.com/stretchr/testify/suite"
)

type PreparationSuite struct {
	suite.Suite
	home, xdg, data string
}

func TestPreparationSuite(t *testing.T) { suite.Run(t, new(PreparationSuite)) }

func (s *PreparationSuite) SetupTest() {
	s.home = s.T().TempDir()
	s.xdg = filepath.Join(s.home, "xdg")
	s.data = filepath.Join(s.xdg, "dotm", "data")
	s.T().Setenv("HOME", s.home)
	s.T().Setenv("XDG_CONFIG_HOME", s.xdg)
	s.T().Setenv("XDG_DATA_HOME", filepath.Join(s.home, "data"))
}

func (s *PreparationSuite) options(configuration, repository string) Options {
	return Options{Configuration: configuration, Repository: repository}
}

func (s *PreparationSuite) write(path, content string, mode os.FileMode) {
	s.T().Helper()
	s.Require().NoError(os.MkdirAll(filepath.Dir(path), 0o700))
	s.Require().NoError(os.WriteFile(path, []byte(content), mode))
}

func (s *PreparationSuite) apply(p *Preparation) (Result, error) {
	s.T().Helper()
	return p.Apply(context.Background(), func(context.Context, Conflict) (bool, error) { return true, nil })
}

func (s *PreparationSuite) TestResolutionPriorityAndSingleton() {
	ctx := context.Background()
	configurations := filepath.Join(s.data, "configurations.toml")
	local := filepath.Join(s.xdg, "dotm", "config.toml")
	s.write(configurations, `[[configurations]]
name = "one"
apps = []
backend = "chezmoi"
repository = "https://one.invalid/dots.git"
`, 0o600)
	p, err := Plan(ctx, s.options("", ""))
	s.Require().NoError(err)
	s.Equal("one", p.Configuration)
	s.Equal("https://one.invalid/dots.git", p.Repository)
	s.write(local, "configuration = \"local-choice\"\n", 0o600)
	s.write(configurations, `[[configurations]]
name = "one"
apps = []
backend = "chezmoi"
repository = "https://one.invalid/dots.git"
[[configurations]]
name = "local-choice"
apps = []
backend = "chezmoi"
repository = "https://local.invalid/dots.git"
`, 0o600)
	p, err = Plan(ctx, s.options("", ""))
	s.Require().NoError(err)
	s.Equal("local-choice", p.Configuration)
	s.Equal("https://local.invalid/dots.git", p.Repository)
	p, err = Plan(ctx, s.options("one", "https://override.invalid/dots.git"))
	s.Require().NoError(err)
	s.Equal("one", p.Configuration)
	s.Equal("https://override.invalid/dots.git", p.Repository)
	s.NotEmpty(p.Conflicts())
}

func (s *PreparationSuite) TestAmbiguousAbsentAndUnknownNames() {
	ctx := context.Background()
	_, err := Plan(ctx, s.options("", ""))
	s.Require().Error(err)
	s.Contains(err.Error(), "--configuration")
	s.write(filepath.Join(s.data, "configurations.toml"), `[[configurations]]
name="a"
apps=[]
[[configurations]]
name="b"
apps=[]
`, 0o600)
	_, err = Plan(ctx, s.options("", ""))
	s.Require().Error(err)
	s.Contains(err.Error(), "--configuration")
	_, err = Plan(ctx, s.options("missing", "https://example.invalid/dots.git"))
	s.Error(err)
}

func (s *PreparationSuite) TestApplyReloadAndNoopRepeat() {
	ctx := context.Background()
	options := s.options("machine", "https://example.invalid/dots.git")
	p, err := Plan(ctx, options)
	s.Require().NoError(err)
	s.NotEmpty(p.Effects())
	s.NoFileExists(filepath.Join(s.data, "catalog.toml"))
	s.NoFileExists(filepath.Join(s.data, "configurations.toml"))
	result, err := s.apply(p)
	s.Require().NoError(err)
	s.Equal(p.Effects(), result.Completed)
	loaded, err := manifest.Load(s.data)
	s.Require().NoError(err)
	s.Contains(loaded.Configs, "machine")
	second, err := Plan(ctx, options)
	s.Require().NoError(err)
	s.Empty(second.Effects())
	result, err = s.apply(second)
	s.Require().NoError(err)
	s.Empty(result.Completed)
}

func (s *PreparationSuite) TestRefusalAndEOFDoNotWrite() {
	options := s.options("machine", "https://new.invalid/dots.git")
	s.write(filepath.Join(s.data, "configurations.toml"), `[[configurations]]
name="machine"
apps=[]
backend="chezmoi"
repository="https://old.invalid/dots.git"
`, 0o600)
	p, err := Plan(context.Background(), options)
	s.Require().NoError(err)
	result, err := p.Apply(context.Background(), func(context.Context, Conflict) (bool, error) { return false, nil })
	s.Error(err)
	s.Empty(result.Completed)
	content, readErr := os.ReadFile(filepath.Join(s.data, "configurations.toml"))
	s.Require().NoError(readErr)
	s.Contains(string(content), "https://old.invalid/dots.git")
	p, err = Plan(context.Background(), options)
	s.Require().NoError(err)
	result, err = p.Apply(context.Background(), func(context.Context, Conflict) (bool, error) { return false, io.EOF })
	s.ErrorIs(err, io.EOF)
	s.Empty(result.Completed)
	content, readErr = os.ReadFile(filepath.Join(s.data, "configurations.toml"))
	s.Require().NoError(readErr)
	s.Contains(string(content), "https://old.invalid/dots.git")
}

func (s *PreparationSuite) TestMalformedCatalogAndUnselectedConfigurationFail() {
	s.write(filepath.Join(s.data, "catalog.toml"), "[broken\n", 0o600)
	_, err := Plan(context.Background(), s.options("machine", "https://example.invalid/dots.git"))
	s.Error(err)
	s.write(filepath.Join(s.data, "catalog.toml"), "", 0o600)
	s.write(filepath.Join(s.data, "configurations.toml"),
		"[[configurations]]\nname=\"other\"\napps=[\"missing-app\"]\n", 0o600)
	_, err = Plan(context.Background(), s.options("machine", "https://example.invalid/dots.git"))
	s.Error(err)
	s.write(filepath.Join(s.data, "configurations.toml"), "", 0o600)
	s.write(filepath.Join(s.xdg, "dotm", "files.toml"), "[unmanaged\n", 0o600)
	_, err = Plan(context.Background(), s.options("machine", "https://example.invalid/dots.git"))
	s.Error(err)
}

func (s *PreparationSuite) TestExistingApplicationsContentAndModesPreserved() {
	catalog := "[editor]\nnix = [\"vim\"]\n"
	configurations := `[[configurations]]
name="machine"
apps=["editor"]
backend="chezmoi"
repository="https://old.invalid/dots.git"
`
	catalogPath := filepath.Join(s.data, "catalog.toml")
	configPath := filepath.Join(s.data, "configurations.toml")
	selectionPath := filepath.Join(s.xdg, "dotm", "config.toml")
	s.write(catalogPath, catalog, 0o640)
	s.write(configPath, configurations, 0o640)
	s.write(selectionPath, "configuration = \"machine\"\n", 0o640)
	p, err := Plan(context.Background(), s.options("machine", "https://new.invalid/dots.git"))
	s.Require().NoError(err)
	result, err := s.apply(p)
	s.Require().NoError(err)
	s.NotEmpty(result.Completed)
	gotCatalog, err := os.ReadFile(catalogPath)
	s.Require().NoError(err)
	s.Equal(catalog, string(gotCatalog))
	gotConfig, err := os.ReadFile(configPath)
	s.Require().NoError(err)
	s.Contains(string(gotConfig), "apps=[\"editor\"]")
	s.Contains(string(gotConfig), "https://new.invalid/dots.git")
	loaded, err := manifest.Load(s.data)
	s.Require().NoError(err)
	s.Equal([]string{"editor"}, loaded.Configs["machine"].Apps)
	for _, path := range []string{catalogPath, configPath, selectionPath} {
		info, statErr := os.Stat(path)
		s.Require().NoError(statErr)
		s.Equal(os.FileMode(0o640), info.Mode().Perm())
	}
}

func (s *PreparationSuite) TestPartialMissingInputsAndSymlinkedDataDirectory() {
	shared := filepath.Join(s.home, "shared")
	s.Require().NoError(os.MkdirAll(shared, 0o700))
	link := filepath.Join(s.home, "linked-data")
	s.Require().NoError(os.Symlink(shared, link))
	p, err := Plan(context.Background(), Options{
		DataDir: link, Configuration: "machine", Repository: "https://example.invalid/dots.git",
	})
	s.Require().NoError(err)
	result, err := s.apply(p)
	s.Require().NoError(err)
	s.NotEmpty(result.Completed)
	s.FileExists(filepath.Join(shared, "catalog.toml"))
	s.FileExists(filepath.Join(shared, "configurations.toml"))
	s.FileExists(filepath.Join(s.xdg, "dotm", "config.toml"))
	s.FileExists(filepath.Join(s.xdg, "dotm", "files.toml"))
}

func (s *PreparationSuite) TestPartialExistingInputsArePreserved() {
	catalogPath := filepath.Join(s.data, "catalog.toml")
	catalog := "[editor]\nnix=[\"vim\"]\n"
	s.write(catalogPath, catalog, 0o640)
	p, err := Plan(context.Background(), s.options("machine", "https://example.invalid/dots.git"))
	s.Require().NoError(err)
	result, err := s.apply(p)
	s.Require().NoError(err)
	s.NotEmpty(result.Completed)
	got, err := os.ReadFile(catalogPath)
	s.Require().NoError(err)
	s.Equal(catalog, string(got))
	for _, path := range []string{
		filepath.Join(s.xdg, "dotm", "config.toml"), filepath.Join(s.xdg, "dotm", "files.toml"),
	} {
		info, statErr := os.Stat(path)
		s.Require().NoError(statErr)
		s.Equal(os.FileMode(0o600), info.Mode().Perm())
	}
}

func (s *PreparationSuite) TestTargetedRepositoryEditsPreserveTOMLForms() {
	cases := []struct {
		name, input string
		preserve    []string
	}{
		{
			name: "inline configuration array",
			input: "# preserve leading comment\n" +
				"configurations = [{ name = \"machine\", apps = [\"editor\"], " +
				"backend = \"chezmoi\", repository = \"https://old.invalid/dots.git\" }]\n",
			preserve: []string{"# preserve leading comment", "apps = [\"editor\"]", "backend = \"chezmoi\""},
		},
		{
			name: "flat source fields",
			input: `[[configurations]]
name="machine"
apps=["editor"]
backend="chezmoi"
repository="https://old.invalid/dots.git"
# retain trailing note
`,
			preserve: []string{"apps=[\"editor\"]", "# retain trailing note", "backend=\"chezmoi\""},
		},
		{
			name:     "add absent source fields",
			input:    "[[configurations]]\nname=\"machine\"\napps=[\"editor\"]\n# keep unrelated comment\n",
			preserve: []string{"apps=[\"editor\"]", "# keep unrelated comment"},
		},
		{
			name:     "add source settings to inline configuration",
			input:    "# retain inline comment\nconfigurations = [{ name = \"machine\", apps = [\"editor\"] }]\n",
			preserve: []string{"# retain inline comment", "apps = [\"editor\"]"},
		},
		{
			name: "multiline repository",
			input: `[[configurations]]
name="machine"
apps=["editor"]
backend="chezmoi"
repository="""https://old.invalid/dots.git"""
`,
			preserve: []string{"apps=[\"editor\"]", "backend=\"chezmoi\""},
		},
		{
			name: "edit only selected profile",
			input: "# shared profiles\n[[configurations]]\nname='other'\napps=[]\n" +
				"backend='chezmoi'\nrepository='https://other.invalid/dots.git' # retain other\n" +
				"[[configurations]]\nname='machine'\napps=['editor']\n" +
				"backend='chezmoi'\nrepository='https://old.invalid/dots.git' # retain selected\n" +
				"[[configurations]]\nname='last'\napps=[] # retain last\n",
			preserve: []string{
				"# shared profiles", "repository='https://other.invalid/dots.git' # retain other",
				"apps=['editor']", "# retain selected", "name='last'\napps=[] # retain last",
			},
		},
		{
			name: "add source only to selected profile",
			input: "[[configurations]]\nname='machine'\napps=['editor'] # selected apps\n" +
				"[[configurations]]\nname='other'\napps=[]\n" +
				"backend='chezmoi'\nrepository='https://other.invalid/dots.git' # untouched\n",
			preserve: []string{
				"apps=['editor'] # selected apps",
				"name='other'\napps=[]\nbackend='chezmoi'\nrepository='https://other.invalid/dots.git' # untouched",
			},
		},
		{
			name: "fill empty source fields",
			input: "[[configurations]]\nname='machine'\napps=['editor']\n" +
				"repository='' # keep repository comment\nbackend='' # keep backend comment\n",
			preserve: []string{"# keep repository comment", "# keep backend comment", "apps=['editor']"},
		},
		{
			name:     "fill empty inline backend",
			input:    "configurations = [{name='machine', apps=['editor'], backend=''}]\n",
			preserve: []string{"apps=['editor']"},
		},
	}
	for _, tc := range cases {
		s.Run(tc.name, func() {
			s.Require().NoError(os.RemoveAll(s.data))
			s.write(filepath.Join(s.data, "catalog.toml"), "[editor]\nnix=[\"vim\"]\n", 0o640)
			configPath := filepath.Join(s.data, "configurations.toml")
			s.write(configPath, tc.input, 0o640)
			p, err := Plan(context.Background(), s.options("machine", "https://new.invalid/dots.git"))
			s.Require().NoError(err)
			_, err = s.apply(p)
			s.Require().NoError(err)
			got, err := os.ReadFile(configPath)
			s.Require().NoError(err)
			s.Contains(string(got), "https://new.invalid/dots.git")
			for _, retained := range tc.preserve {
				s.Contains(string(got), retained)
			}
			info, err := os.Stat(configPath)
			s.Require().NoError(err)
			s.Equal(os.FileMode(0o640), info.Mode().Perm())
		})
	}
}

func (s *PreparationSuite) TestSelectionEditPreservesQuotedKeyAndComments() {
	s.write(filepath.Join(s.data, "catalog.toml"), "", 0o600)
	s.write(filepath.Join(s.data, "configurations.toml"), `[[configurations]]
name="machine"
apps=[]
[[configurations]]
name="other"
apps=[]
`, 0o600)
	selection := filepath.Join(s.xdg, "dotm", "config.toml")
	s.write(selection, "# selection note\n'configuration' = 'machine' # keep inline note\n", 0o640)
	p, err := Plan(context.Background(), s.options("other", "https://example.invalid/dots.git"))
	s.Require().NoError(err)
	_, err = s.apply(p)
	s.Require().NoError(err)
	got, err := os.ReadFile(selection)
	s.Require().NoError(err)
	s.Contains(string(got), "# selection note")
	s.Contains(string(got), "# keep inline note")
	s.Contains(string(got), "'other'")
}

func (s *PreparationSuite) TestRetargetedDataDirectorySymlinkRejectsStalePlan() {
	first, second := filepath.Join(s.home, "first"), filepath.Join(s.home, "second")
	s.Require().NoError(os.MkdirAll(first, 0o700))
	s.Require().NoError(os.MkdirAll(second, 0o700))
	link := filepath.Join(s.home, "data-link")
	s.Require().NoError(os.Symlink(first, link))
	p, err := Plan(context.Background(), Options{
		DataDir: link, Configuration: "machine", Repository: "https://example.invalid/dots.git",
	})
	s.Require().NoError(err)
	s.Require().NoError(os.Remove(link))
	s.Require().NoError(os.Symlink(second, link))
	_, err = s.apply(p)
	s.Error(err)
	s.NoFileExists(filepath.Join(first, "catalog.toml"))
	s.NoFileExists(filepath.Join(second, "catalog.toml"))
}

func (s *PreparationSuite) TestLateWriteFailureReportsCompletedEffects() {
	if os.Geteuid() == 0 {
		s.T().Skip("directory permission failure is not effective for root")
	}
	dotm := filepath.Join(s.xdg, "dotm")
	s.Require().NoError(os.MkdirAll(dotm, 0o700))
	data := filepath.Join(s.home, "shared-data")
	catalogPath := filepath.Join(data, "catalog.toml")
	configurationsPath := filepath.Join(data, "configurations.toml")
	s.write(catalogPath, "", 0o600)
	s.write(configurationsPath, `[[configurations]]
name="machine"
apps=[]
backend="chezmoi"
repository="https://old.invalid/dots.git"
`, 0o600)
	s.write(filepath.Join(dotm, "config.toml"), "configuration=\"machine\"\n", 0o600)
	p, err := Plan(context.Background(), Options{
		DataDir: data, Configuration: "machine", Repository: "https://new.invalid/dots.git",
	})
	s.Require().NoError(err)
	s.Require().NoError(os.Chmod(dotm, 0o500))
	defer func() { _ = os.Chmod(dotm, 0o700) }()
	result, err := p.Apply(context.Background(), func(context.Context, Conflict) (bool, error) { return true, nil })
	s.Error(err)
	s.Require().Len(result.Completed, 1)
	s.Equal(configurationsPath, result.Completed[0].Path)
	s.FileExists(configurationsPath)
	s.NoFileExists(filepath.Join(dotm, "files.toml"))
}

func (s *PreparationSuite) TestUnsafeFileSymlinkAndDanglingPathFail() {
	dotm := filepath.Join(s.xdg, "dotm")
	s.Require().NoError(os.MkdirAll(dotm, 0o700))
	target := filepath.Join(s.home, "outside")
	s.write(target, "configuration = \"outside\"\n", 0o600)
	s.Require().NoError(os.Symlink(target, filepath.Join(dotm, "config.toml")))
	_, err := Plan(context.Background(), s.options("machine", "https://example.invalid/dots.git"))
	s.Error(err)
	s.Require().NoError(os.Remove(filepath.Join(dotm, "config.toml")))
	s.Require().NoError(os.Symlink(filepath.Join(s.home, "absent"), filepath.Join(dotm, "files.toml")))
	_, err = Plan(context.Background(), s.options("machine", "https://example.invalid/dots.git"))
	s.Error(err)
}

func (s *PreparationSuite) TestStalePlanRejectedWithoutWrites() {
	p, err := Plan(context.Background(), s.options("machine", "https://example.invalid/dots.git"))
	s.Require().NoError(err)
	s.write(filepath.Join(s.xdg, "dotm", "files.toml"), "[unmanaged]\n", 0o600)
	_, err = s.apply(p)
	s.Error(err)
	s.NoFileExists(filepath.Join(s.data, "catalog.toml"))
}

func (s *PreparationSuite) TestCallbackErrorDoesNotWrite() {
	s.write(filepath.Join(s.data, "configurations.toml"), `[[configurations]]
name="machine"
apps=[]
backend="chezmoi"
repository="https://old.invalid/dots.git"
`, 0o600)
	options := s.options("machine", "https://new.invalid/dots.git")
	p, err := Plan(context.Background(), options)
	s.Require().NoError(err)
	want := errors.New("confirmation failed")
	result, err := p.Apply(context.Background(), func(context.Context, Conflict) (bool, error) { return false, want })
	s.ErrorIs(err, want)
	s.Empty(result.Completed)
	s.NoFileExists(filepath.Join(s.data, "catalog.toml"))
}

func (s *PreparationSuite) TestNilAndCanceledConfirmationDoNotWrite() {
	configs := filepath.Join(s.data, "configurations.toml")
	s.write(configs, `[[configurations]]
name="machine"
apps=[]
backend="chezmoi"
repository="https://old.invalid/dots.git"
`, 0o600)
	options := s.options("machine", "https://new.invalid/dots.git")
	p, err := Plan(context.Background(), options)
	s.Require().NoError(err)
	_, err = p.Apply(context.Background(), nil)
	s.Error(err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := p.Apply(ctx, func(context.Context, Conflict) (bool, error) {
		s.Fail("confirmation must not run after cancellation")
		return true, nil
	})
	s.ErrorIs(err, context.Canceled)
	s.Empty(result.Completed)
	content, err := os.ReadFile(configs)
	s.Require().NoError(err)
	s.Contains(string(content), "https://old.invalid/dots.git")
}
