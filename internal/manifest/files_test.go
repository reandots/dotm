package manifest

import (
	"os"
	"path/filepath"
)

func (s *ManifestSuite) TestCatalogFileOverlapRules() {
	s.T().Setenv("HOME", "/home/files-test")
	s.T().Setenv("XDG_CONFIG_HOME", "")
	for _, test := range []struct {
		name, catalog string
		severity      FileSeverity
	}{
		{"aliases", "[a]\nconfigs = ['~/.config/a', '$XDG_CONFIG_HOME/a/']", FileWarning},
		{"same app nested", "[a]\nconfigs = ['/absent/a', '/absent/a/child']", FileWarning},
		{"cross app equal", "[a]\nconfigs = ['/absent/a']\n[b]\nconfigs = ['/absent/a/']", FileError},
		{"cross app nested", "[a]\nconfigs = ['/absent/a/child']\n[b]\nconfigs = ['/absent/a']", FileError},
		{"same app contradiction", "[a]\nconfigs = ['/absent/a']\nunmanaged = ['/absent/a/data']", FileError},
		{"cross app contradiction", "[a]\nconfigs = ['/absent/a']\n[b]\nunmanaged = ['/absent/a']", FileError},
		{"unmanaged overlap", "[a]\nunmanaged = ['/absent/a']\n[b]\nunmanaged = ['/absent/a/data']", FileWarning},
		{"component boundary", "[a]\nconfigs = ['/absent/a']\n[b]\nconfigs = ['/absent/ab']", ""},
		{"root coverage", "[a]\nconfigs = ['/']\n[b]\nconfigs = ['/absent']", FileError},
	} {
		s.Run(test.name, func() {
			data, err := Load(s.fixture(test.catalog, ""))
			if test.severity == FileError {
				s.Require().Error(err)
				s.Nil(data)
				s.Contains(err.Error(), "error:")
				s.Contains(err.Error(), "configs[0]")
				s.Contains(err.Error(), "overlaps")
				return
			}
			s.Require().NoError(err)
			if test.severity == "" {
				s.Empty(data.FileDiagnostics)
				return
			}
			s.Require().Len(data.FileDiagnostics, 1)
			diagnostic := data.FileDiagnostics[0]
			s.Equal(FileWarning, diagnostic.Severity)
			s.Require().NotNil(diagnostic.Related)
			s.Equal(filepath.Join(data.CatalogDir, "catalog.toml"), diagnostic.Declaration.Source)
			s.NotEmpty(diagnostic.Declaration.Input)
			s.NotEmpty(diagnostic.Related.Input)
		})
	}
}

func (s *ManifestSuite) TestFilesSelectionAndStructuralParents() {
	s.T().Setenv("HOME", "/home/files-test")
	s.T().Setenv("XDG_CONFIG_HOME", "")
	dir := s.fixture(`
[root]
dependencies = ['dep']
[dep]
configs = ['~/.config/dep']
unmanaged = ['$XDG_CACHE_HOME/dep']
[other]
configs = ['~/.config/other']
`, "")
	data, err := Load(dir)
	s.Require().NoError(err)
	local := filepath.Join(dir, "files.toml")
	s.Require().NoError(os.WriteFile(local, []byte(`[unmanaged]
"~/.config" = ["other", "unknown"]
"$XDG_CACHE_HOME" = ["dep"]
`), 0o600))
	model, err := data.Files([]string{"root"}, local)
	s.Require().NoError(err)
	s.NoError(model.Err(), "nonselected ownership does not prohibit local acknowledgement")
	s.Require().Len(model.Diagnostics, 1)
	s.Equal(FileWarning, model.Diagnostics[0].Severity)
	s.Equal("unmanaged", model.Diagnostics[0].Declaration.Field)
	s.Require().Len(model.Declarations, 6)
	s.True(model.Declarations[0].Selected, "dependency belongs to selected closure")
	s.False(model.Declarations[2].Selected, "other application stays in model without selection")
	s.Equal("/home/files-test/.config/other", model.Declarations[4].Path)
	s.Equal("~/.config", model.Declarations[4].Parent)
	s.Equal("other", model.Declarations[4].Input)
	for _, declaration := range model.Declarations {
		s.NotEqual("/home/files-test/.config", declaration.Path, "structural parent is not an exclusion")
	}
	other, err := data.Files([]string{"other"}, local)
	s.Require().NoError(err)
	s.Require().Error(other.Err())
	s.Contains(other.Err().Error(), `application "other": configs[0]`)
	s.Contains(other.Err().Error(), `unmanaged["~/.config"][0]`)
	s.False(data.FileDeclarations[0].Selected, "selection does not mutate catalog provenance")
}

func (s *ManifestSuite) TestLocalFileValidationAndLiteralNames() {
	s.T().Setenv("HOME", "/home/files-test")
	s.T().Setenv("XDG_CONFIG_HOME", "")
	data, err := Load(s.fixture("", ""))
	s.Require().NoError(err)
	local := filepath.Join(s.T().TempDir(), "files.toml")
	model, err := data.Files(nil, local)
	s.Require().NoError(err)
	s.NoError(model.Err())
	_, err = os.Stat(local)
	s.ErrorIs(err, os.ErrNotExist, "loading never creates user declarations")
	for _, content := range []string{
		"[unmanaged]\n'/tmp' = 'child'", "[unmanaged]\n'/tmp' = [42]",
		"[unknown]\nvalue = []", "unmanaged = []", "[broken",
	} {
		s.Run(content, func() {
			s.Require().NoError(os.WriteFile(local, []byte(content), 0o600))
			_, loadErr := data.Files(nil, local)
			s.Require().Error(loadErr)
			s.Contains(loadErr.Error(), local)
		})
	}
	for _, content := range []string{
		"[unmanaged]\n'relative' = []", "[unmanaged]\n'~someone' = ['x']",
		"[unmanaged]\n'/tmp' = ['', '.', '..', 'a/b']",
	} {
		s.Run(content, func() {
			s.Require().NoError(os.WriteFile(local, []byte(content), 0o600))
			invalid, loadErr := data.Files(nil, local)
			s.Require().NoError(loadErr)
			s.Error(invalid.Err())
			s.Empty(invalid.Declarations)
		})
	}
	s.Require().NoError(os.WriteFile(local, []byte(`[unmanaged]
"~/literal" = ["*", "$HOME", "file with spaces", "child", "child"]
"~/literal/child" = ["nested"]
`), 0o600))
	model, err = data.Files(nil, local)
	s.Require().NoError(err)
	s.NoError(model.Err())
	s.Equal("/home/files-test/literal/*", model.Declarations[0].Path)
	s.Equal("/home/files-test/literal/$HOME", model.Declarations[1].Path)
	s.Require().Len(model.Diagnostics, 3, "duplicate plus both nested overlap pairs warn")

	for _, diagnostic := range model.Diagnostics {
		s.Equal(FileWarning, diagnostic.Severity)
	}
	path, err := FilesPath()
	s.Require().NoError(err)
	s.Equal("/home/files-test/.config/dotm/files.toml", path)
}

func (s *ManifestSuite) TestGeneralDiagnosticOmitsDeclarationProvenance() {
	for _, path := range []string{"", "/state/dotm/chezmoi-policy.json"} {
		diagnostic := FileDiagnostic{Severity: FileError, Path: path, Message: "policy binding mismatch"}
		text := diagnostic.String()
		s.Contains(text, "error:")
		s.Contains(text, diagnostic.Message)
		if path != "" {
			s.Contains(text, path)
		}
		s.NotContains(text, `[""]`)
		s.NotContains(text, "[0]")
		s.NotContains(text, "()")
	}
}

func (s *ManifestSuite) TestUnmanagedValidationAndExpansion() {
	s.T().Setenv("HOME", "/home/files-test")
	s.T().Setenv("XDG_STATE_HOME", "")
	data, err := Load(s.fixture(`[a]
unmanaged = ['$XDG_STATE_HOME/a/../a', '${XDG_STATE_HOME:-$HOME/.local/state}/a']
`, ""))
	s.Require().NoError(err)
	s.Equal([]string{"/home/files-test/.local/state/a"}, data.Apps["a"].Unmanaged)
	s.Require().Len(data.FileDeclarations, 2)
	s.Require().Len(data.FileDiagnostics, 1)
	s.Equal(FileWarning, data.FileDiagnostics[0].Severity)
	for _, value := range []string{"42", "['relative']", "['~other/a']", "['$(pwd)']", "[42]", "['']"} {
		s.Run(value, func() {
			_, loadErr := Load(s.fixture("[a]\nunmanaged = "+value, ""))
			s.Require().Error(loadErr)
			s.Contains(loadErr.Error(), "unmanaged")
		})
	}
}

func (s *ManifestSuite) TestDeclarationComparisonDoesNotFollowSymlinks() {
	dir := s.T().TempDir()
	target := filepath.Join(dir, "real")
	alias := filepath.Join(dir, "alias")
	s.Require().NoError(os.Mkdir(target, 0o700))
	s.Require().NoError(os.Symlink(target, alias))
	data, err := Load(s.fixture(
		"[a]\nconfigs = ['"+target+"']\n[b]\nconfigs = ['"+alias+"/missing']", ""))
	s.Require().NoError(err)
	s.Empty(data.FileDiagnostics, "physical aliases are not lexical ownership overlaps")
}
