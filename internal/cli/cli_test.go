package cli_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dotm/internal/cli"

	"github.com/stretchr/testify/suite"
)

type CLISuite struct {
	suite.Suite
	dataDir  string
	stateDir string
}

func TestCLISuite(t *testing.T) { suite.Run(t, new(CLISuite)) }

func (s *CLISuite) SetupTest() {
	base := s.T().TempDir()
	s.dataDir = filepath.Join(base, "data")
	s.stateDir = filepath.Join(base, "state")
	configDir := filepath.Join(base, "config")
	s.Require().NoError(os.MkdirAll(s.dataDir, 0o700))
	s.Require().NoError(os.MkdirAll(filepath.Join(configDir, "dotm"), 0o700))
	s.Require().NoError(os.WriteFile(filepath.Join(s.dataDir, "catalog.toml"), nil, 0o600))
	s.Require().NoError(os.WriteFile(
		filepath.Join(s.dataDir, "configurations.toml"),
		[]byte("[[configurations]]\nname = \"empty\"\napps = []\n"), 0o600))
	s.Require().NoError(os.WriteFile(
		filepath.Join(configDir, "dotm", "config.toml"), []byte("configuration = \"empty\"\n"), 0o600))
	s.T().Setenv("XDG_CONFIG_HOME", configDir)
	s.T().Setenv("XDG_STATE_HOME", s.stateDir)
	s.T().Setenv("PATH", "")
}

func (s *CLISuite) TestEmptyCombinedEnvironmentCreatesNoStateWithoutNix() {
	err := cli.Run([]string{"-riu", "--yes", "--data-dir", s.dataDir}, strings.NewReader("q\n"), io.Discard, io.Discard)
	s.Require().NoError(err)
	_, err = os.Lstat(s.stateDir)
	s.ErrorIs(err, os.ErrNotExist)
}

func (s *CLISuite) TestAmbiguousOrUnscopedMutationRequestsAreRejected() {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"flags-with-mutation-command", []string{"-i", "update"}},
		{"inspection-with-mutation-flag", []string{"status", "-r"}},
		{"approval-without-removal", []string{"install", "--yes"}},
		{"cleanup-with-operation-flag", []string{"clean", "-u"}},
		{"negative-retention", []string{"clean", "--keep=-1"}},
		{"cleanup-option-with-removal", []string{"remove-extras", "--gc"}},
		{"no-operation", nil},
		{"unexpected-operand", []string{"show", "extra"}},
		{"unknown-nested-command", []string{"update", "unknown"}},
		{"missing-files-command", []string{"files"}},
		{"missing-cfg-command", []string{"cfg"}},
		{"unknown-command", []string{"unknown"}},
		{"unknown-shell", []string{"completion", "unknown"}},
		{"extra-shell-operand", []string{"completion", "bash", "extra"}},
	} {
		s.Run(tc.name, func() {
			args := append([]string{"--data-dir", s.dataDir}, tc.args...)
			err := cli.Run(args, strings.NewReader("yes\n"), io.Discard, io.Discard)
			s.Error(err)
			_, err = os.Lstat(s.stateDir)
			s.ErrorIs(err, os.ErrNotExist)
		})
	}
}

func (s *CLISuite) TestCleanupDoesNotRequireManifestsOrSelection() {
	s.Require().NoError(os.RemoveAll(s.dataDir))
	s.T().Setenv("XDG_CONFIG_HOME", filepath.Join(s.dataDir, "absent-config"))
	for _, args := range [][]string{{"clean", "--dry-run"}, {"--yes", "clean", "--keep=0"}} {
		s.Require().NoError(cli.Run(args, strings.NewReader(""), io.Discard, io.Discard))
		_, err := os.Lstat(s.stateDir)
		s.ErrorIs(err, os.ErrNotExist)
	}
}

func (s *CLISuite) TestDefaultDataDirectoryKeepsSelectionMachineLocal() {
	for _, configMode := range []string{"custom-xdg", "home-fallback"} {
		s.Run(configMode, func() {
			home := s.T().TempDir()
			s.T().Setenv("HOME", home)
			configHome := filepath.Join(home, ".config")
			if configMode == "custom-xdg" {
				configHome = filepath.Join(home, "custom-config")
				s.T().Setenv("XDG_CONFIG_HOME", configHome)
			} else {
				s.T().Setenv("XDG_CONFIG_HOME", "")
			}
			localDir := filepath.Join(configHome, "dotm")
			s.Require().NoError(os.MkdirAll(localDir, 0o700))
			dataDir := filepath.Join(localDir, "data")
			s.Require().NoError(os.MkdirAll(dataDir, 0o700))
			for _, name := range []string{"catalog.toml", "configurations.toml"} {
				content, err := os.ReadFile(filepath.Join(s.dataDir, name))
				s.Require().NoError(err)
				// #nosec G703 -- Private test directory and fixed manifest names, not caller-controlled paths.
				s.Require().NoError(os.WriteFile(filepath.Join(dataDir, name), content, 0o600))
			}
			s.Require().NoError(os.WriteFile(filepath.Join(localDir, "config.toml"),
				[]byte("configuration = 'empty'\n"), 0o600))
			var out bytes.Buffer
			s.Require().NoError(cli.Run([]string{"show"}, strings.NewReader(""), &out, io.Discard))
			s.Contains(out.String(), "empty")
		})
	}
}

func (s *CLISuite) TestExplicitDataDirectoryDoesNotRequireValidXDGForListing() {
	s.T().Setenv("XDG_CONFIG_HOME", "relative-config")
	var out bytes.Buffer
	s.Require().NoError(cli.Run([]string{"configs", "--data-dir", s.dataDir},
		strings.NewReader(""), &out, io.Discard))
	s.Contains(out.String(), "empty")
	s.ErrorContains(cli.Run([]string{"configs"}, strings.NewReader(""), io.Discard, io.Discard),
		"configuration directory must be absolute")
}

func (s *CLISuite) TestVerbosityKeepsReadinessVisibleWithoutBackendDump() {
	s.Require().NoError(os.WriteFile(filepath.Join(s.dataDir, "catalog.toml"),
		[]byte("[dependency]\nnix=['fd']\n[consumer]\ndependencies=['dependency']\n"), 0o600))
	s.Require().NoError(os.WriteFile(filepath.Join(s.dataDir, "configurations.toml"),
		[]byte("[[configurations]]\nname='empty'\napps=['consumer']\n"), 0o600))
	for _, args := range [][]string{{"status"}, {"-v", "status"}, {"status", "--verbose"}} {
		var output bytes.Buffer
		invocation := append([]string{"--data-dir", s.dataDir}, args...)
		s.Require().NoError(cli.Run(invocation, strings.NewReader(""), &output, io.Discard))
		text := output.String()
		s.Contains(text, "dependency")
		s.Contains(text, "missing")
		s.Contains(text, "blocked by dependency")
		s.NotContains(text, "\x1b[")
		if len(args) == 1 {
			s.NotContains(text, "Installed profile:")
			s.NotContains(text, "Installed entries:")
		} else {
			s.Contains(text, "Installed profile:")
			s.Contains(text, "Installed entries:")
		}
	}
}

func (s *CLISuite) TestVerboseCombinedFlagsPreserveOperationOrder() {
	var output, diagnostics bytes.Buffer
	err := cli.Run([]string{"--data-dir", s.dataDir, "-viur"},
		strings.NewReader(""), &output, &diagnostics)
	s.Require().NoError(err)
	s.Contains(output.String(), "Installed profile:")
	text := diagnostics.String()
	update := strings.Index(text, "Operation: update")
	install := strings.Index(text, "Operation: install")
	remove := strings.Index(text, "Operation: remove-extras")
	s.GreaterOrEqual(update, 0)
	s.Greater(install, update)
	s.Greater(remove, install)
}

func (s *CLISuite) TestHelpAndCompletionDoNotNeedUserSetup() {
	s.Require().NoError(os.RemoveAll(s.dataDir))
	s.T().Setenv("XDG_CONFIG_HOME", "invalid-relative-path")
	for _, args := range [][]string{
		{"--help"},
		{"help", "files"},
		{"update", "--help"},
		{"update", "cfg", "--help"},
		{"completion", "bash"},
		{"completion", "zsh"},
		{"completion", "fish"},
		{"completion", "powershell"},
		{"__complete", "files", ""},
	} {
		s.Run(strings.Join(args, " "), func() {
			var out, stderr bytes.Buffer
			s.Require().NoError(cli.Run(args, strings.NewReader(""), &out, &stderr))
			_, err := os.Lstat(s.stateDir)
			s.ErrorIs(err, os.ErrNotExist)
		})
	}
}

func (s *CLISuite) TestCompletionOffersCommandsAndDirectoryFlags() {
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{""}, []string{"update\t", "files\t", "completion\t", ":4\n"}},
		{[]string{"files", ""}, []string{"lint\t", "refresh\t", "status\t", ":4\n"}},
		{[]string{"update", ""}, []string{"cfg\t", ":4\n"}},
		{[]string{"--data-dir", ""}, []string{":16\n"}},
		{[]string{"cfg", "lint", "--source", ""}, []string{":16\n"}},
	} {
		s.Run(strings.Join(tc.args, " "), func() {
			var out bytes.Buffer
			err := cli.Run(append([]string{"__complete"}, tc.args...), strings.NewReader(""), &out, io.Discard)
			s.Require().NoError(err)
			for _, want := range tc.want {
				s.Contains(out.String(), want)
			}
		})
	}
}

func (s *CLISuite) TestGlobalOptionsBeforeAndAfterNestedCommands() {
	for _, args := range [][]string{
		{"--data-dir", s.dataDir, "-v", "update", "cfg"},
		{"update", "--data-dir", s.dataDir, "cfg", "-v"},
		{"update", "cfg", "--data-dir", s.dataDir, "-v"},
	} {
		s.Require().NoError(cli.Run(args, strings.NewReader(""), io.Discard, io.Discard))
	}
}

type closedWriter struct{}

func (closedWriter) Write(_ []byte) (int, error) { return 0, io.ErrClosedPipe }

func (s *CLISuite) TestCompletionReturnsOutputErrors() {
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		s.Run(shell, func() {
			err := cli.Run([]string{"completion", shell}, strings.NewReader(""), closedWriter{}, io.Discard)
			s.ErrorIs(err, io.ErrClosedPipe)
			s.Equal(3, cli.ExitCode(err))
		})
	}
}
