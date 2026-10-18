package chezmoi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"
)

type sourceRegressionSuite struct {
	suite.Suite
	root   string
	remote string
}

func TestSourceRegressionSuite(t *testing.T) {
	suite.Run(t, new(sourceRegressionSuite))
}

func (s *sourceRegressionSuite) SetupTest() {
	s.root = s.T().TempDir()
	s.remote = filepath.Join(s.root, "remote.git")
	s.T().Setenv("HOME", filepath.Join(s.root, "home"))
	s.T().Setenv("XDG_CONFIG_HOME", filepath.Join(s.root, "config"))
	s.sourceGit("", "init", "--bare", "--initial-branch=main", s.remote)
}

func (s *sourceRegressionSuite) sourceGit(dir string, args ...string) string {
	cmd := exec.CommandContext(context.Background(), "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	data, err := cmd.CombinedOutput()
	s.Require().NoError(err, "git %v: %s", args, data)
	return strings.TrimSpace(string(data))
}

func (s *sourceRegressionSuite) seedRemote() {
	work := filepath.Join(s.root, "seed")
	s.sourceGit("", "init", "-b", "main", work)
	s.sourceGit(work, "config", "user.name", "Source Test")
	s.sourceGit(work, "config", "user.email", "source-test@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "tracked.txt"), []byte("one\n"), 0o600); err != nil {
		s.Require().NoError(err)
	}
	s.sourceGit(work, "add", "tracked.txt")
	s.sourceGit(work, "commit", "-m", "initial")
	s.sourceGit(work, "remote", "add", "origin", s.remote)
	s.sourceGit(work, "push", "-u", "origin", "main")
}

func (s *sourceRegressionSuite) cloneSource(path string) {
	s.sourceGit("", "clone", s.remote, path)
}

func (s *sourceRegressionSuite) TestSourcePath() {
	home := filepath.Join(s.root, "home")
	data := filepath.Join(s.root, "data")
	s.T().Setenv("HOME", home)
	s.T().Setenv("XDG_DATA_HOME", "")
	got, err := SourcePath()
	s.Require().NoError(err)
	s.Equal(filepath.Join(home, ".local", "share", "chezmoi"), got)
	s.T().Setenv("XDG_DATA_HOME", data)
	got, err = SourcePath()
	s.Require().NoError(err)
	s.Equal(filepath.Join(data, "chezmoi"), got)
	s.T().Setenv("XDG_DATA_HOME", "relative")
	_, err = SourcePath()
	s.Error(err)
}

func (s *sourceRegressionSuite) TestCloneUnchangedAndFastForward() {
	s.seedRemote()
	path := filepath.Join(s.root, "checkout")
	manager := sourceManager{path: path, rename: os.Rename}
	result, err := manager.sync(context.Background(), s.remote, nil)
	s.Require().NoError(err)
	s.True(result.Changed)
	s.Equal(path, result.Path)
	result, err = manager.sync(context.Background(), s.remote, nil)
	s.Require().NoError(err)
	s.False(result.Changed)
	work := filepath.Join(s.root, "seed")
	if writeErr := os.WriteFile(filepath.Join(work, "tracked.txt"), []byte("two\n"), 0o600); writeErr != nil {
		s.Require().NoError(writeErr)
	}
	s.sourceGit(work, "commit", "-am", "second")
	s.sourceGit(work, "push")
	result, err = manager.sync(context.Background(), s.remote, nil)
	s.Require().NoError(err)
	s.True(result.Changed)
	data, err := os.ReadFile(filepath.Join(path, "tracked.txt"))
	s.Require().NoError(err)
	s.Equal("two\n", string(data))
}

func (s *sourceRegressionSuite) TestMatchingDirtyCheckoutPreservesFiles() {
	s.seedRemote()
	for _, kind := range []string{"tracked", "untracked", "ignored", "empty-directory"} {
		s.Run(kind, func() {
			path := filepath.Join(s.root, "checkout-"+kind)
			s.cloneSource(path)
			name := "local.txt"
			contents := "local data\n"
			switch kind {
			case "tracked":
				name, contents = "tracked.txt", "modified\n"
			case "ignored":
				info := filepath.Join(path, ".git", "info", "exclude")
				if err := os.WriteFile(info, []byte("local.txt\n"), 0o600); err != nil {
					s.Require().NoError(err)
				}
			case "empty-directory":
				if err := os.MkdirAll(filepath.Join(path, "empty", "nested"), 0o700); err != nil {
					s.Require().NoError(err)
				}
				_, err := (sourceManager{path: path, rename: os.Rename}).sync(context.Background(), s.remote, nil)
				s.Error(err)
				s.Require().ErrorContains(err, "empty")
				s.Contains(err.Error(), path)
				s.Contains(err.Error(), "empty directories")
				s.DirExists(filepath.Join(path, "empty", "nested"))
				return
			}
			file := filepath.Join(path, name)
			if err := os.WriteFile(file, []byte(contents), 0o600); err != nil {
				s.Require().NoError(err)
			}
			_, err := (sourceManager{path: path, rename: os.Rename}).sync(context.Background(), s.remote, nil)
			s.Error(err)
			s.Require().ErrorContains(err, name)
			s.Contains(err.Error(), path)
			s.Contains(err.Error(), "commit intended source changes")
			data, readErr := os.ReadFile(file)
			s.Require().NoError(readErr)
			s.Equal(contents, string(data))
		})
	}
}

func (s *sourceRegressionSuite) TestHiddenIndexAndShallowCheckoutRefused() {
	s.seedRemote()
	for _, kind := range []string{"assume-unchanged", "skip-worktree"} {
		s.Run(kind, func() {
			path := filepath.Join(s.root, "checkout-"+kind)
			s.cloneSource(path)
			flag := "--assume-unchanged"
			if kind == "skip-worktree" {
				flag = "--skip-worktree"
			}
			s.sourceGit(path, "update-index", flag, "tracked.txt")
			_, err := (sourceManager{path: path, rename: os.Rename}).sync(context.Background(), s.remote, nil)
			s.Error(err)
		})
	}
	path := filepath.Join(s.root, "checkout-shallow")
	s.sourceGit("", "clone", "--depth=1", "file://"+s.remote, path)
	_, err := (sourceManager{path: path, rename: os.Rename}).sync(context.Background(), s.remote, nil)
	s.Error(err)
}

func (s *sourceRegressionSuite) TestUnsafeMatchingHistoryRefused() {
	s.seedRemote()
	for _, kind := range []string{"diverged", "ahead", "no-upstream", "detached"} {
		s.Run(kind, func() {
			path := filepath.Join(s.root, "checkout-"+kind)
			s.cloneSource(path)
			s.sourceGit(path, "config", "user.name", "Source Test")
			s.sourceGit(path, "config", "user.email", "source-test@example.invalid")
			switch kind {
			case "diverged", "ahead":
				if kind == "diverged" {
					work := filepath.Join(s.root, "seed")
					if err := os.WriteFile(filepath.Join(work, "tracked.txt"), []byte("remote\n"), 0o600); err != nil {
						s.Require().NoError(err)
					}
					s.sourceGit(work, "commit", "-am", "remote")
					s.sourceGit(work, "push")
				}
				if err := os.WriteFile(filepath.Join(path, "tracked.txt"), []byte("local\n"), 0o600); err != nil {
					s.Require().NoError(err)
				}
				s.sourceGit(path, "commit", "-am", "local")
			case "no-upstream":
				s.sourceGit(path, "branch", "--unset-upstream")
			case "detached":
				s.sourceGit(path, "checkout", "--detach")
			}
			_, err := (sourceManager{path: path, rename: os.Rename}).sync(context.Background(), s.remote, nil)
			s.Error(err)
		})
	}
}

func (s *sourceRegressionSuite) TestUnexpectedRootsRefused() {
	path := filepath.Join(s.root, "unexpected")
	s.Require().NoError(os.Mkdir(path, 0o700))
	_, err := (sourceManager{path: path, rename: os.Rename}).sync(context.Background(), s.remote, nil)
	s.Error(err)
	link := filepath.Join(s.root, "linked")
	s.Require().NoError(os.Symlink(path, link))
	_, err = (sourceManager{path: link, rename: os.Rename}).sync(context.Background(), s.remote, nil)
	s.Error(err)
}

func (s *sourceRegressionSuite) TestGitHooksAreNotRun() {
	s.seedRemote()
	path := filepath.Join(s.root, "checkout")
	marker := filepath.Join(s.root, "hook-ran")
	s.cloneSource(path)
	hook := filepath.Join(path, ".git", "hooks", "post-merge")
	body := "#!/bin/sh\nprintf ran > " + marker + "\n"
	//nolint:gosec // Git hook fixture must be executable.
	s.Require().NoError(os.WriteFile(hook, []byte(body), 0o700))
	work := filepath.Join(s.root, "seed")
	if err := os.WriteFile(filepath.Join(work, "tracked.txt"), []byte("two\n"), 0o600); err != nil {
		s.Require().NoError(err)
	}
	s.sourceGit(work, "commit", "-am", "second")
	s.sourceGit(work, "push")
	_, err := (sourceManager{path: path, rename: os.Rename}).sync(context.Background(), s.remote, nil)
	s.Require().NoError(err)
	_, err = os.Stat(marker)
	s.True(os.IsNotExist(err))
}

func (s *sourceRegressionSuite) TestDirtyRenameQuotesBothBlockingPaths() {
	s.seedRemote()
	path := filepath.Join(s.root, "checkout")
	s.cloneSource(path)
	name := "renamed\nfile"
	s.sourceGit(path, "mv", "tracked.txt", name)
	_, err := (sourceManager{path: path, rename: os.Rename}).sync(context.Background(), s.remote, nil)
	s.Require().Error(err)
	s.Contains(err.Error(), strconv.Quote("tracked.txt"))
	s.Contains(err.Error(), strconv.Quote(name))
	s.NotContains(err.Error(), name)
	s.FileExists(filepath.Join(path, name))
}
