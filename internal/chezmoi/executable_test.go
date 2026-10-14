package chezmoi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"dotm/internal/manifest"

	"github.com/stretchr/testify/suite"
)

type executableSuite struct{ suite.Suite }

func TestExecutableSuite(t *testing.T) { suite.Run(t, new(executableSuite)) }

func (s *executableSuite) TestMalformedResponseIsCorrectiveFailure() {
	bad := filepath.Join(s.T().TempDir(), "bad")
	// #nosec G306 -- Private executable compatibility fixture.
	s.Require().NoError(os.WriteFile(bad, []byte("#!/bin/sh\nprintf 'not-json'\n"), 0o700))
	err := ProbeExecutable(context.Background(), bad)
	s.ErrorContains(err, "incompatible with dotm")
	s.ErrorContains(err, "repair/remove")
}

func (s *executableSuite) TestRealProbeIsolationAndExplicitApplicationWithoutPATH() {
	backend, err := exec.LookPath("chezmoi")
	if err != nil {
		s.T().Skip("real chezmoi required")
	}
	root := s.T().TempDir()
	home, source := filepath.Join(root, "home"), filepath.Join(root, "source")
	s.Require().NoError(os.MkdirAll(home, 0o700))
	s.Require().NoError(os.MkdirAll(source, 0o700))
	config := filepath.Join(root, "config")
	s.Require().NoError(os.MkdirAll(filepath.Join(config, "chezmoi"), 0o700))
	configPath := filepath.Join(config, "chezmoi", "chezmoi.toml")
	s.Require().NoError(os.WriteFile(configPath, []byte("invalid toml ["), 0o600))
	s.T().Setenv("HOME", home)
	s.T().Setenv("XDG_CONFIG_HOME", config)
	s.T().Setenv("XDG_CONFIG_DIRS", filepath.Join(root, "system-config"))
	s.T().Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	s.T().Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	s.T().Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	s.T().Setenv("PATH", root)
	// The caller's incompatible settings must not affect capability probing.
	s.Require().NoError(ProbeExecutable(context.Background(), backend))
	data, err := os.ReadFile(configPath)
	s.Require().NoError(err)
	s.Equal("invalid toml [", string(data))
	entries, err := os.ReadDir(home)
	s.Require().NoError(err)
	s.Empty(entries)
	s.Require().NoError(os.WriteFile(configPath, []byte("mode = \"file\"\n"), 0o600))
	s.Require().NoError(os.WriteFile(filepath.Join(source, "dot_backend-test"), []byte("managed\n"), 0o600))
	result, err := Apply(WithExecutable(context.Background(), backend), source,
		[]manifest.ConfigTarget{{Path: filepath.Join(home, ".backend-test"), Applications: []string{"demo"}}}, nil)
	s.Require().NoError(err)
	s.Empty(result.Diagnostics)
	data, err = os.ReadFile(filepath.Join(home, ".backend-test"))
	s.Require().NoError(err)
	s.Equal("managed\n", string(data))
}
