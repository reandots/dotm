package chezmoi

import (
	"context"
	"os"
	"path/filepath"
)

func (s *sourceRegressionSuite) TestPublicSourceUsesSharedPathAndLocalRepositoryIdentity() {
	s.seedRemote()
	s.T().Setenv("XDG_DATA_HOME", filepath.Join(s.root, "data"))
	// Repository-routing environment must not redirect Git away from the source.
	s.T().Setenv("GIT_DIR", filepath.Join(s.root, "unrelated.git"))
	result, err := SyncSource(context.Background(), "file://"+s.remote, nil)
	s.Require().NoError(err)
	s.Equal(filepath.Join(s.root, "data", "chezmoi"), result.Path)
	s.Equal("one\n", s.sourceRead(filepath.Join(result.Path, "tracked.txt")))
	called := false
	result, err = SyncSource(context.Background(), s.remote, func(context.Context, Replacement) (bool, error) {
		called = true
		return false, nil
	})
	s.Require().NoError(err)
	s.False(called)
	s.False(result.Changed)
	s.False(result.Cancelled)
}

func (s *sourceRegressionSuite) TestCancelledContextDoesNotCreateSource() {
	path := filepath.Join(s.root, "absent")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := (sourceManager{path: path, rename: os.Rename}).sync(ctx, s.remote, nil)
	s.ErrorIs(err, context.Canceled)
	s.True(result.Cancelled)
	_, statErr := os.Lstat(path)
	s.ErrorIs(statErr, os.ErrNotExist)
}

func (s *sourceRegressionSuite) sourceRead(path string) string {
	s.T().Helper()
	data, err := os.ReadFile(path)
	s.Require().NoError(err)
	return string(data)
}

func (s *sourceRegressionSuite) TestBranchMergeOptionsCannotSquashFastForward() {
	s.seedRemote()
	path := filepath.Join(s.root, "checkout")
	s.cloneSource(path)
	s.sourceGit(path, "config", "branch.main.mergeOptions", "--squash")
	s.sourceGit(path, "config", "merge.autoStash", "true")
	seed := filepath.Join(s.root, "seed")
	s.Require().NoError(os.WriteFile(filepath.Join(seed, "tracked.txt"), []byte("advanced\n"), 0o600))
	s.sourceGit(seed, "commit", "-am", "advance")
	s.sourceGit(seed, "push")
	result, err := (sourceManager{path: path, rename: os.Rename}).sync(context.Background(), s.remote, nil)
	s.Require().NoError(err)
	s.True(result.Changed)
	s.Equal(s.sourceGit(seed, "rev-parse", "HEAD"), s.sourceGit(path, "rev-parse", "HEAD"))
	s.Empty(s.sourceGit(path, "status", "--porcelain"))
}
