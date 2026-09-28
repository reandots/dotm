package chezmoi

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"
)

type replacementRegressionSuite struct {
	suite.Suite
	root, source, oldRemote, newRemote string
}

func TestReplacementRegressionSuite(t *testing.T) {
	suite.Run(t, new(replacementRegressionSuite))
}

func (s *replacementRegressionSuite) SetupTest() {
	s.root = s.T().TempDir()
	home := filepath.Join(s.T().TempDir(), "home")
	config := filepath.Join(home, ".config")
	s.Require().NoError(os.MkdirAll(config, 0o700))
	s.T().Setenv("HOME", home)
	s.T().Setenv("XDG_CONFIG_HOME", config)
	s.source = filepath.Join(s.root, "shared-source")
	s.oldRemote = filepath.Join(s.root, "old.git")
	s.newRemote = filepath.Join(s.root, "new.git")
	s.git("", "init", "--bare", s.oldRemote)
	s.git("", "init", "--bare", s.newRemote)
	seed := filepath.Join(s.root, "seed")
	s.git("", "init", "-b", "main", seed)
	s.git(seed, "config", "user.name", "replacement test")
	s.git(seed, "config", "user.email", "replacement@example.invalid")
	s.write(filepath.Join(seed, "tracked.txt"), "old source\n")
	s.git(seed, "add", "tracked.txt")
	s.git(seed, "commit", "-m", "old source")
	s.git(seed, "remote", "add", "origin", s.oldRemote)
	s.git(seed, "push", "-u", "origin", "main")
	s.git("", "--git-dir", s.oldRemote, "symbolic-ref", "HEAD", "refs/heads/main")
	s.git("", "clone", s.oldRemote, s.source)
	s.git(s.source, "config", "user.name", "replacement test")
	s.git(s.source, "config", "user.email", "replacement@example.invalid")
	// Replacement remotes use distinguishable content and an independently
	// published commit, so the confirmation path is exercised end to end.
	newSeed := filepath.Join(s.root, "new-seed")
	s.git("", "init", "-b", "main", newSeed)
	s.git(newSeed, "config", "user.name", "replacement test")
	s.git(newSeed, "config", "user.email", "replacement@example.invalid")
	s.write(filepath.Join(newSeed, "tracked.txt"), "new source\n")
	s.git(newSeed, "add", "tracked.txt")
	s.git(newSeed, "commit", "-m", "new source")
	s.git(newSeed, "remote", "add", "origin", s.newRemote)
	s.git(newSeed, "push", "-u", "origin", "main")
	s.git("", "--git-dir", s.newRemote, "symbolic-ref", "HEAD", "refs/heads/main")
}

func (s *replacementRegressionSuite) git(dir string, args ...string) string {
	s.T().Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	s.Require().NoError(err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

func (s *replacementRegressionSuite) write(path, content string) {
	s.T().Helper()
	s.Require().NoError(os.WriteFile(path, []byte(content), 0o600))
}

func (s *replacementRegressionSuite) sync(confirm ConfirmReplacement) (SourceResult, error) {
	s.T().Helper()
	return (sourceManager{path: s.source, rename: os.Rename}).sync(context.Background(), s.newRemote, confirm)
}

func (s *replacementRegressionSuite) assertOriginal() {
	s.T().Helper()
	content, err := os.ReadFile(filepath.Join(s.source, "tracked.txt"))
	s.Require().NoError(err)
	s.Equal("old source\n", string(content))
	remote, err := git(context.Background(), s.source, "remote", "get-url", "origin")
	s.Require().NoError(err)
	s.Equal(s.oldRemote, remote)
}

func (s *replacementRegressionSuite) TestConfirmedReplacementRetainsBackup() {
	called := false
	result, err := s.sync(func(_ context.Context, replacement Replacement) (bool, error) {
		called = true
		s.Equal(s.source, replacement.Path)
		s.Equal(s.oldRemote, replacement.OldRepository)
		s.Equal(s.newRemote, replacement.Repository)
		return true, nil
	})
	s.Require().NoError(err)
	s.True(called)
	s.True(result.Changed)
	s.NotEmpty(result.Backup)
	content, err := os.ReadFile(filepath.Join(s.source, "tracked.txt"))
	s.Require().NoError(err)
	s.Equal("new source\n", string(content))
	oldContent, err := os.ReadFile(filepath.Join(result.Backup, "tracked.txt"))
	s.Require().NoError(err)
	s.Equal("old source\n", string(oldContent))
}

func (s *replacementRegressionSuite) TestRefusalNilAndConfirmationErrorPreserveOriginal() {
	tests := []struct {
		name      string
		confirm   ConfirmReplacement
		cancelled bool
		wantErr   bool
	}{
		{name: "refused", confirm: func(context.Context, Replacement) (bool, error) {
			return false, nil
		}, cancelled: true},
		{name: "nil", cancelled: true},
		{name: "error", confirm: func(context.Context, Replacement) (bool, error) {
			return false, errors.New("confirmation failed")
		}, cancelled: true, wantErr: true},
	}
	for _, test := range tests {
		s.Run(test.name, func() {
			s.SetupTest()
			result, err := s.sync(test.confirm)
			if test.wantErr {
				s.Error(err)
			} else {
				s.NoError(err)
			}
			s.Equal(test.cancelled, result.Cancelled)
			s.assertOriginal()
		})
	}
}

func (s *replacementRegressionSuite) TestCloneFailurePreservesOriginal() {
	// A local bare path that cannot be cloned is an isolated deterministic failure.
	result, err := (sourceManager{path: s.source, rename: os.Rename}).sync(
		context.Background(), filepath.Join(s.root, "missing.git"),
		func(context.Context, Replacement) (bool, error) { return true, nil })
	s.Error(err)
	s.False(result.Changed)
	s.assertOriginal()
}

func (s *replacementRegressionSuite) TestCallbackMutationsCannotAuthorizeUnsafeReplacement() {
	tests := []struct {
		name   string
		mutate func()
	}{
		{name: "tracked", mutate: func() {
			s.write(filepath.Join(s.source, "tracked.txt"), "callback mutation\n")
		}},
		{name: "untracked", mutate: func() {
			s.write(filepath.Join(s.source, "callback.txt"), "callback data\n")
		}},
		{name: "local ref", mutate: func() {
			s.git(s.source, "branch", "callback-branch", "HEAD")
		}},
		{name: "origin changed", mutate: func() {
			s.git(s.source, "remote", "set-url", "origin", s.newRemote)
		}},
	}
	for _, test := range tests {
		s.Run(test.name, func() {
			s.SetupTest()
			_, err := s.sync(func(context.Context, Replacement) (bool, error) {
				test.mutate()
				return true, nil
			})
			s.Error(err)
			wantTracked := "old source\n"
			if test.name == "tracked" {
				wantTracked = "callback mutation\n"
			}
			s.Equal(wantTracked, s.read(filepath.Join(s.source, "tracked.txt")))
			if test.name == "untracked" {
				s.Equal("callback data\n", s.read(filepath.Join(s.source, "callback.txt")))
			}
		})
	}
}

func (s *replacementRegressionSuite) read(path string) string {
	s.T().Helper()
	data, err := os.ReadFile(path)
	s.Require().NoError(err)
	return string(data)
}

func (s *replacementRegressionSuite) TestRenameFailuresPreserveRecoverableData() {
	tests := []struct {
		name         string
		failCalls    map[int]bool
		retryBlocked bool
	}{
		{name: "first rename", failCalls: map[int]bool{1: true}},
		{name: "replacement rename", failCalls: map[int]bool{2: true}},
		{name: "rollback failure", failCalls: map[int]bool{2: true, 3: true}, retryBlocked: true},
		{name: "journal rename failure", failCalls: map[int]bool{3: true}, retryBlocked: true},
	}
	for _, test := range tests {
		s.Run(test.name, func() {
			s.SetupTest()
			calls := 0
			manager := sourceManager{path: s.source, rename: func(old, new string) error {
				calls++
				if test.failCalls[calls] {
					return errors.New("injected rename failure")
				}
				return os.Rename(old, new)
			}}
			result, err := manager.sync(context.Background(), s.newRemote,
				func(context.Context, Replacement) (bool, error) { return true, nil })
			s.Error(err)
			oldPath := filepath.Join(s.source, "tracked.txt")
			if result.Backup != "" {
				oldPath = filepath.Join(result.Backup, "tracked.txt")
			}
			s.Equal("old source\n", s.read(oldPath))
			_, retryErr := (sourceManager{path: s.source, rename: os.Rename}).sync(context.Background(), s.newRemote,
				func(context.Context, Replacement) (bool, error) { return true, nil })
			if test.retryBlocked {
				s.Error(retryErr)
			} else {
				s.NoError(retryErr)
			}
		})
	}
}

func (s *replacementRegressionSuite) TestLocalBlobRefsAndReflogOnlyCommitsRefuse() {
	tests := []struct {
		name  string
		setup func()
	}{
		{name: "blob ref", setup: func() {
			blob := s.gitInput("blob evidence\n", s.source, "hash-object", "-w", "--stdin")
			s.git(s.source, "update-ref", "refs/dotm/blob", blob)
		}},
		{name: "annotated local tag", setup: func() {
			s.git(s.source, "tag", "-a", "local-only", "-m", "not pushed", "HEAD")
		}},
		{name: "reflog-only commit", setup: func() {
			s.write(filepath.Join(s.source, "reflog.txt"), "unpublished\n")
			s.git(s.source, "add", "reflog.txt")
			s.git(s.source, "commit", "-m", "reflog-only")
			s.git(s.source, "reset", "--hard", "origin/main")
		}},
	}
	for _, test := range tests {
		s.Run(test.name, func() {
			s.SetupTest()
			test.setup()
			called := false
			_, err := s.sync(func(context.Context, Replacement) (bool, error) { called = true; return true, nil })
			s.Error(err)
			s.False(called)
		})
	}
}

func (s *replacementRegressionSuite) gitInput(input, dir string, args ...string) string {
	s.T().Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	s.Require().NoError(err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

func (s *replacementRegressionSuite) TestUntrackedIgnoredAndEmptyDirectoriesRefuse() {
	tests := []struct {
		name, path, content string
	}{
		{name: "untracked", path: "local.txt", content: "local\n"},
		{name: "ignored", path: "ignored.txt", content: "ignored\n"},
		{name: "empty directory", path: "empty/nested"},
	}
	for _, test := range tests {
		s.Run(test.name, func() {
			s.SetupTest()
			path := filepath.Join(s.source, test.path)
			if test.name == "empty directory" {
				s.Require().NoError(os.MkdirAll(path, 0o700))
			} else {
				if test.name == "ignored" {
					s.write(filepath.Join(s.source, ".git", "info", "exclude"), "ignored.txt\n")
				}
				s.write(path, test.content)
			}
			called := false
			_, err := s.sync(func(context.Context, Replacement) (bool, error) { called = true; return true, nil })
			s.Error(err)
			s.False(called)
			if test.name == "empty directory" {
				info, statErr := os.Stat(path)
				s.NoError(statErr)
				s.True(info.IsDir())
			} else {
				s.Equal(test.content, s.read(path))
			}
		})
	}
}

func (s *replacementRegressionSuite) TestUnreachableOldOriginRefusesConfirmation() {
	s.git("", "--git-dir", s.oldRemote, "update-ref", "-d", "refs/heads/main")
	called := false
	_, err := s.sync(func(context.Context, Replacement) (bool, error) { called = true; return true, nil })
	s.Error(err)
	s.False(called)
	s.Equal("old source\n", s.read(filepath.Join(s.source, "tracked.txt")))
}

func (s *replacementRegressionSuite) TestUnpublishedBranchesAndDetachedCommitsRefuseReplacement() {
	s.Run("noncurrent branch", func() {
		s.SetupTest()
		s.git(s.source, "checkout", "-b", "private")
		s.write(filepath.Join(s.source, "private.txt"), "not published\n")
		s.git(s.source, "add", "private.txt")
		s.git(s.source, "commit", "-m", "private commit")
		s.git(s.source, "checkout", "main")
		called := false
		_, err := s.sync(func(context.Context, Replacement) (bool, error) { called = true; return true, nil })
		s.Error(err)
		s.False(called)
		s.assertOriginal()
	})
	s.Run("detached local commit", func() {
		s.SetupTest()
		s.write(filepath.Join(s.source, "detached.txt"), "detached\n")
		s.git(s.source, "add", "detached.txt")
		s.git(s.source, "commit", "-m", "detached commit")
		head := s.git(s.source, "rev-parse", "HEAD")
		s.git(s.source, "checkout", "--detach", head)
		called := false
		_, err := s.sync(func(context.Context, Replacement) (bool, error) { called = true; return true, nil })
		s.Error(err)
		s.False(called)
		s.Equal("detached\n", s.read(filepath.Join(s.source, "detached.txt")))
	})
}
