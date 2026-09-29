package chezmoi

import (
	"os"
	"path/filepath"
)

func (s *validationSuite) userConfig(name, contents string) string {
	s.T().Helper()
	dir := filepath.Join(s.root, "config", "chezmoi")
	s.Require().NoError(os.MkdirAll(dir, 0o700))
	path := filepath.Join(dir, name)
	s.Require().NoError(os.WriteFile(path, []byte(contents), 0o600))
	return path
}

func (s *validationSuite) TestUserSettingsFormatsAndSourceConflict() {
	for _, tc := range []struct {
		name string
		data string
	}{
		{"toml", "sourceDir = '/elsewhere'"},
		{"yaml", "sourceDir: /elsewhere"},
		{"yml", "sourceDir: /elsewhere"},
		{"json", `{"sourceDir":"/elsewhere"}`},
		{"jsonc", "{ // comment\n\"sourceDir\": \"/elsewhere\",\n}"},
	} {
		s.Run(tc.name, func() {
			path := s.userConfig("chezmoi."+tc.name, tc.data)
			result := s.validate()
			s.Require().Len(result.Diagnostics, 1)
			s.Equal("settings.sourceDir", result.Diagnostics[0].Code)
			s.Equal(path, result.Diagnostics[0].Path)
			s.Require().NoError(os.Remove(path))
		})
	}
}

func (s *validationSuite) TestUserActionsAndMalformedSettings() {
	for _, tc := range []struct {
		name string
		data string
		code string
	}{
		{"hooks", "[Hooks.apply.pre]\ncommand = 'never-execute'", "settings.hooks"},
		{"hook type", "hooks = 'invalid'", "settings.hooks"},
		{"ambiguous hooks", "[Hooks]\n[hooks.apply.pre]\ncommand = 'never-execute'", "settings.hooks"},
		{"source type", "SourceDir = []", "settings.sourceDir"},
		{"git add", "[git]\nautoAdd = true", "settings.git"},
		{"git commit", "[GIT]\nAUTOCOMMIT = true", "settings.git"},
		{"git push", "[git]\nautoPush = true", "settings.git"},
		{"git type", "[git]\nautoPush = 'yes'", "settings.git"},
		{"parse", "sourceDir = [", "settings.parse"},
	} {
		s.Run(tc.name, func() {
			path := s.userConfig("chezmoi.toml", tc.data)
			before := s.snapshot()
			result := s.validate()
			s.Require().Len(result.Diagnostics, 1)
			s.Equal(tc.code, result.Diagnostics[0].Code)
			s.Equal(path, result.Diagnostics[0].Path)
			s.Equal(before, s.snapshot())
		})
	}
}

func (s *validationSuite) TestUserConfigSelectionAndPriority() {
	s.userConfig("chezmoi.toml", "")
	other := s.userConfig("chezmoi.jsonc", "{}")
	result := s.validate()
	s.Require().Len(result.Diagnostics, 1)
	s.Equal("settings.selection", result.Diagnostics[0].Code)
	s.Require().NoError(os.Remove(other))
	systemConfig := filepath.Join(s.root, "system-config", "chezmoi")
	s.Require().NoError(os.MkdirAll(systemConfig, 0o700))
	s.Require().NoError(os.WriteFile(filepath.Join(systemConfig, "chezmoi.toml"),
		[]byte("[hooks.apply.pre]\ncommand = 'not-effective'"), 0o600))
	s.Empty(s.validate().Diagnostics, "a higher-priority user config replaces the system config")
	s.Require().NoError(os.Remove(filepath.Join(s.root, "config", "chezmoi", "chezmoi.toml")))
	result = s.validate()
	s.Require().Len(result.Diagnostics, 1)
	s.Equal("settings.hooks", result.Diagnostics[0].Code)
}

func (s *validationSuite) TestMatchingSourceAndInactiveSettingsAreAllowed() {
	s.userConfig("chezmoi.toml", "sourceDir = '~/source'\n[hooks]\n[git]\nautoAdd = false\n")
	s.Empty(s.validate().Diagnostics)
}
