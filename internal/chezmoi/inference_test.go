package chezmoi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"
)

type inferenceSuite struct {
	suite.Suite
	root string
}

func TestInferenceSuite(t *testing.T) {
	suite.Run(t, new(inferenceSuite))
}

func (s *inferenceSuite) SetupTest() {
	s.root = s.T().TempDir()
	s.T().Setenv("HOME", filepath.Join(s.root, "home"))
	s.T().Setenv("GIT_CONFIG_NOSYSTEM", "1")
	s.T().Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
}

func (s *inferenceSuite) git(dir string, args ...string) string {
	cmd := exec.CommandContext(context.Background(), "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	data, err := cmd.CombinedOutput()
	s.Require().NoError(err, "git %v: %s", args, data)
	return string(data)
}

func (s *inferenceSuite) checkout() (string, string) {
	remote := filepath.Join(s.root, "remote.git")
	s.git("", "init", "--bare", "--initial-branch=main", remote)
	seed := filepath.Join(s.root, "seed")
	s.git("", "init", "-b", "main", seed)
	s.git(seed, "config", "user.name", "Inference Test")
	s.git(seed, "config", "user.email", "inference@example.invalid")
	if err := os.WriteFile(filepath.Join(seed, "tracked"), []byte("data\n"), 0o600); err != nil {
		s.Require().NoError(err)
	}
	s.git(seed, "add", "tracked")
	s.git(seed, "commit", "-m", "initial")
	s.git(seed, "remote", "add", "origin", remote)
	s.git(seed, "push", "-u", "origin", "main")
	checkout := filepath.Join(s.root, "checkout")
	s.git("", "clone", remote, checkout)
	return checkout, remote
}

func (s *inferenceSuite) TestInfersLocalOrigin() {
	checkout, remote := s.checkout()
	got, err := InferRepository(context.Background(), checkout)
	s.Require().NoError(err)
	s.Equal(remote, got)
}

func (s *inferenceSuite) TestRejectsMultipleOriginURLs() {
	checkout, _ := s.checkout()
	s.git(checkout, "config", "--add", "remote.origin.url", filepath.Join(s.root, "other.git"))
	_, err := InferRepository(context.Background(), checkout)
	s.Error(err)
	s.Contains(err.Error(), "exactly one")
}

func (s *inferenceSuite) TestRejectsUnsafeSourceAndCheckout() {
	_, err := InferRepository(context.Background(), filepath.Join(s.root, "missing"))
	s.Error(err)
	nonCheckout := filepath.Join(s.root, "plain")
	s.Require().NoError(os.Mkdir(nonCheckout, 0o700))
	_, err = InferRepository(context.Background(), nonCheckout)
	s.Error(err)
	checkout, _ := s.checkout()
	s.Require().NoError(os.WriteFile(filepath.Join(checkout, "untracked"), []byte("unsafe"), 0o600))
	_, err = InferRepository(context.Background(), checkout)
	s.Error(err)
	s.Contains(err.Error(), "dirty")
}
