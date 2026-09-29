package chezmoi

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"
)

type validationSuite struct {
	suite.Suite
	root   string
	source string
}

func TestValidationSuite(t *testing.T) {
	suite.Run(t, new(validationSuite))
}

func (s *validationSuite) SetupTest() {
	s.root = s.T().TempDir()
	s.source = filepath.Join(s.root, "source")
	s.Require().NoError(os.MkdirAll(s.source, 0o700))
	s.T().Setenv("HOME", s.root)
	s.T().Setenv("XDG_CONFIG_HOME", filepath.Join(s.root, "config"))
	s.T().Setenv("XDG_CONFIG_DIRS", filepath.Join(s.root, "system-config"))
	s.T().Setenv("XDG_DATA_HOME", filepath.Join(s.root, "data"))
}

func (s *validationSuite) put(path, content string) {
	s.T().Helper()
	path = filepath.Join(s.source, path)
	s.Require().NoError(os.MkdirAll(filepath.Dir(path), 0o700))
	s.Require().NoError(os.WriteFile(path, []byte(content), 0o600))
}

func (s *validationSuite) validate() ValidationResult {
	s.T().Helper()
	result, err := ValidateSource(context.Background(), s.source)
	s.Require().NoError(err)
	return result
}

func (s *validationSuite) TestSemanticAttributesAndLiteralEscapes() {
	cases := []struct {
		name      string
		directory bool
		forbidden bool
	}{
		{"run_once_before_script.sh", false, true},
		{"run_onchange_after_script.sh.tmpl", false, true},
		{"run_literal_script.sh", false, true},
		{"modify_private_dot_rc.tmpl", false, true},
		{"remove_dot_old", false, true},
		{"remove_old", true, true},
		{"exact_private_dot_config", true, true},
		{"external_exact_vendor", true, true},
		{"literal_run_script", false, false},
		{"private_literal_modify_script", false, false},
		{"private_run_script", false, false},
		{"executable_modify_file", false, false},
		{"dot_remove_file", false, false},
		{"file_run_script", false, false},
		{"file.tmpl.literal", false, false},
		{"literal_exact_dir", true, false},
		{"private_exact_dir", true, false},
		{"exact_file", false, false},
		{"run_directory", true, false},
	}
	for _, tc := range cases {
		s.Run(tc.name, func() {
			s.source = filepath.Join(s.root, tc.name)
			s.Require().NoError(os.MkdirAll(s.source, 0o700))
			if tc.directory {
				s.Require().NoError(os.Mkdir(filepath.Join(s.source, tc.name), 0o700))
			} else {
				s.put(tc.name, "trusted file contents")
			}
			result := s.validate()
			if tc.forbidden {
				s.Require().NotEmpty(result.Diagnostics)
				s.Equal("source-policy", result.Diagnostics[0].Code)
				s.Equal(filepath.Join(s.source, tc.name), result.Diagnostics[0].Path)
			} else {
				s.Empty(result.Diagnostics)
			}
		})
	}
}

func (s *validationSuite) TestWholeSourceIgnoresNeitherSelectionNorIgnoreRules() {
	s.put(".chezmoiignore", "**\n")
	s.put("selected/dot_rc", "safe\n")
	s.put("unselected/run_bad.sh", "touch SHOULD_NOT_EXIST")
	s.put("ignored/exact_dot_config/file", "safe\n")
	s.put("ignored/.chezmoiremove.tmpl", "{{ output \"touch\" \"SHOULD_NOT_EXIST\" }}")
	result := s.validate()
	s.Len(result.Diagnostics, 3)
}

func (s *validationSuite) TestSpecialMechanismsAndNonExecution() {
	marker := filepath.Join(s.root, "executed")
	payload := "#!/bin/sh\ntouch " + marker + "\n"
	s.put("run_script", payload)
	s.put("modify_file", payload)
	s.put(".chezmoiscripts/nested/run_once_script", payload)
	s.put(".chezmoiexternal.toml.tmpl", "{{ output \"touch\" \""+marker+"\" }}")
	s.put("nested/.chezmoiexternals/download.yaml", "url: https://invalid.example/file\n")
	s.put(".chezmoiremove", "*\n")
	s.put(".chezmoiignore", "{{ output \"touch\" \""+marker+"\" }}")
	s.put("dot_rc.tmpl", "{{ output \"touch\" \""+marker+"\" }}")
	destination := filepath.Join(s.root, "destination")
	s.Require().NoError(os.WriteFile(destination, []byte("keep"), 0o600))
	before := s.snapshot()
	result := s.validate()
	s.Len(result.Diagnostics, 6)
	s.Equal(before, s.snapshot(), "validation must not mutate source, destination, configuration, or state")
	_, err := os.Stat(marker)
	s.ErrorIs(err, os.ErrNotExist)
	data, err := os.ReadFile(destination)
	s.Require().NoError(err)
	s.Equal("keep", string(data))
	data, err = os.ReadFile(filepath.Join(s.source, "run_script"))
	s.Require().NoError(err)
	s.Equal(payload, string(data))
}

func (s *validationSuite) TestSpecialDirectoriesHaveDifferentNameSemantics() {
	s.put(".chezmoitemplates/run_partial", "{{ .someValue }}")
	s.put(".chezmoidata/remove_values.json", "{\"answer\": 42}")
	s.put("external_vendor/run_literal.sh", "literal, not a script")
	s.put("external_vendor/exact_literal/.chezmoiremove", "literal, not a removal rule")
	s.put("literal_.chezmoiremove", "ordinary destination file")
	s.put(".git/run_ignored", "not source input")
	s.Empty(s.validate().Diagnostics)
	s.put(".chezmoiscripts/literal_run_not_script", "not a script")
	result := s.validate()
	s.Require().Len(result.Diagnostics, 1)
	s.Equal("source-syntax", result.Diagnostics[0].Code)
}

func (s *validationSuite) TestRootIsAppliedOnceAndNonSourceTreeIsNotLinted() {
	s.put(".chezmoiroot", " home/nested \n")
	s.put("run_outside_source", "not used")
	s.put("home/nested/.chezmoiroot", "not-recursive")
	s.put("home/nested/literal_run_file", "safe")
	result := s.validate()
	s.Equal(filepath.Join(s.source, "home", "nested"), result.SourceDir)
	s.Empty(result.Diagnostics)
	s.put("home/nested/unselected/run_bad", "not executed")
	s.Len(s.validate().Diagnostics, 1)
}

func (s *validationSuite) TestMalformedRootAndDataAreDiagnostics() {
	for _, root := range []string{"", "..", "sub/../other", "/absolute", "missing"} {
		s.Run(root, func() {
			s.put(".chezmoiroot", root)
			result := s.validate()
			s.Require().NotEmpty(result.Diagnostics)
			s.Equal("source-syntax", result.Diagnostics[0].Code)
		})
	}
	s.put(".chezmoiroot", ".")
	s.put(".chezmoidata.toml", "broken = [")
	s.put(".chezmoidata/values.json", "{broken}")
	s.put(".chezmoi.yaml.tmpl", "hooks: [")
	s.put("literal_", "invalid empty target")
	result := s.validate()
	s.Len(result.Diagnostics, 4)
	for _, diagnostic := range result.Diagnostics {
		s.Equal("source-syntax", diagnostic.Code)
	}
}

func (s *validationSuite) TestDynamicRepositoryConfigIsNotEvaluated() {
	s.put(".chezmoi.toml.tmpl", "{{ output \"touch\" \"never\" }}")
	result := s.validate()
	s.Require().Len(result.Diagnostics, 1)
	s.Equal("source-policy", result.Diagnostics[0].Code)
	s.Contains(result.Diagnostics[0].Message, "bootstrap")
}

func (s *validationSuite) TestSymlinkDirectoryCannotHideActions() {
	s.put("elsewhere/run_action", "not executed")
	s.Require().NoError(os.Symlink(filepath.Join(s.source, "elsewhere"), filepath.Join(s.source, "linked")))
	result := s.validate()
	s.Len(result.Diagnostics, 2)
}

func (s *validationSuite) TestArgumentAndCancellationErrorsAreDistinct() {
	result, err := ValidateSource(context.Background(), "relative")
	s.Error(err)
	s.Empty(result.Diagnostics)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err = ValidateSource(ctx, s.source)
	s.ErrorIs(err, context.Canceled)
	s.Empty(result.Diagnostics)
}

func (s *validationSuite) snapshot() map[string]string {
	s.T().Helper()
	files := make(map[string]string)
	err := filepath.WalkDir(s.root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		files[path] = info.Mode().String()
		if info.Mode().IsRegular() {
			data, readErr := os.ReadFile(path) //nolint:gosec // G122: private test tree, no concurrent writers.
			if readErr != nil {
				return readErr
			}
			files[path] += ":" + string(data)
		}
		return nil
	})
	s.Require().NoError(err)
	return files
}

func (s *validationSuite) TestNestedRootCannotTraverseDirectorySymlink() {
	out := filepath.Join(s.root, "outside")
	s.Require().NoError(os.MkdirAll(filepath.Join(out, "nested"), 0o700))
	s.Require().NoError(os.Symlink(out, filepath.Join(s.source, "link")))
	s.put(".chezmoiroot", "link/nested")
	result := s.validate()
	s.Require().Len(result.Diagnostics, 1)
	s.Equal("source-policy", result.Diagnostics[0].Code)
}

func (s *validationSuite) TestRepositoryConfigChecksHooksAndUsesCheckoutNotNestedRoot() {
	s.put(".chezmoiroot", "home")
	s.put("home/.chezmoi.toml.tmpl", "sourceDir = '"+s.source+"'\n")
	s.Empty(s.validate().Diagnostics)
	s.put("home/.chezmoi.toml.tmpl", "[hooks.apply.pre]\ncommand = 'never-execute'\n")
	result := s.validate()
	s.Require().Len(result.Diagnostics, 1)
	s.Contains(result.Diagnostics[0].Code, "hooks")
}

func (s *validationSuite) TestMultipleRepositoryConfigTemplatesAreSourceError() {
	s.put(".chezmoi.toml.tmpl", "")
	s.put(".chezmoi.jsonc.tmpl", "{ /* ordinary data */ }")
	result := s.validate()
	s.Require().Len(result.Diagnostics, 1)
	s.Equal("source-syntax", result.Diagnostics[0].Code)
	s.Contains(result.Diagnostics[0].Message, "multiple")
}

func (s *validationSuite) TestCustomDelimiterConfigCannotHideGeneratedHooksInComments() {
	s.put(".chezmoi.toml.tmpl", "# chezmoi:template:left-delimiter=[[ right-delimiter=]]\n"+
		"# [[ printf \"\\n[hooks.apply.pre]\\ncommand = 'never-execute'\\n\" ]]\n")
	result := s.validate()
	s.Require().Len(result.Diagnostics, 1)
	s.Equal("source-policy", result.Diagnostics[0].Code)
}
