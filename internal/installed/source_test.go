package installed

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"dotm/internal/manifest"

	"github.com/stretchr/testify/suite"
)

type SourceSuite struct {
	suite.Suite
}

func TestSourceSuite(t *testing.T) {
	suite.Run(t, new(SourceSuite))
}

func (s *SourceSuite) TestMissingLockWithoutInitDoesNotRewriteSource() {
	dir := s.T().TempDir()
	flake := filepath.Join(dir, "flake.nix")
	original := []byte("existing source\n")
	s.Require().NoError(os.WriteFile(flake, original, 0o600))

	err := prepareSource(context.Background(), dir, "x86_64-linux", nil, false, nil)

	s.Error(err)
	got, readErr := os.ReadFile(flake)
	s.Require().NoError(readErr)
	s.Equal(original, got)
	_, statErr := os.Lstat(filepath.Join(dir, "flake.lock"))
	s.ErrorIs(statErr, os.ErrNotExist)
}

func (s *SourceSuite) TestExistingLockIsPreserved() {
	dir := s.T().TempDir()
	lock := filepath.Join(dir, "flake.lock")
	original := []byte("{ \"version\": 7 }\n")
	s.Require().NoError(os.WriteFile(lock, original, 0o600))

	err := prepareSource(context.Background(), dir, "x86_64-linux", []manifest.Package{{Attr: "hello"}}, true, nil)

	s.Require().NoError(err)
	got, readErr := os.ReadFile(lock)
	s.Require().NoError(readErr)
	s.Equal(original, got)
	info, statErr := os.Lstat(lock)
	s.Require().NoError(statErr)
	s.True(info.Mode().IsRegular())
}

func (s *SourceSuite) TestNonregularLockIsRejected() {
	dir := s.T().TempDir()
	target := filepath.Join(dir, "target")
	s.Require().NoError(os.WriteFile(target, []byte("lock data"), 0o600))
	lock := filepath.Join(dir, "flake.lock")
	s.Require().NoError(os.Symlink(target, lock))

	err := prepareSource(context.Background(), dir, "x86_64-linux", nil, true, nil)

	s.Error(err)
	_, statErr := os.Lstat(filepath.Join(dir, "flake.nix"))
	s.ErrorIs(statErr, os.ErrNotExist)
}
