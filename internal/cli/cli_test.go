package cli_test

import (
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
	s.Require().NoError(os.WriteFile(filepath.Join(s.dataDir, "configurations.toml"), []byte("[[configurations]]\nname = \"empty\"\napps = []\n"), 0o600))
	s.Require().NoError(os.WriteFile(filepath.Join(configDir, "dotm", "config.toml"), []byte("configuration = \"empty\"\n"), 0o600))
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
		{"no-operation", nil},
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
