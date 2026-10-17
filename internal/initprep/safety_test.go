package initprep

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func (s *PreparationSuite) TestEveryPartialFileCombination() {
	for mask := range 16 {
		s.Run(fmt.Sprintf("existing-%04b", mask), func() {
			root := s.T().TempDir()
			s.T().Setenv("XDG_CONFIG_HOME", root)
			local := filepath.Join(root, "dotm")
			dataDir := filepath.Join(local, "data")
			inputs := []struct{ path, content string }{
				{filepath.Join(dataDir, "catalog.toml"), "# existing catalog\n[tool]\nnix = ['vim']\n"},
				{filepath.Join(dataDir, "configurations.toml"), "# existing configuration\n" +
					"[[configurations]]\nname = 'machine'\napps = []\nbackend = 'chezmoi'\n" +
					"repository = 'https://example.invalid/dots.git'\n"},
				{filepath.Join(local, "config.toml"), "# existing selection\nconfiguration = 'machine'\n"},
				{filepath.Join(local, "files.toml"), "# existing declarations\n[unmanaged]\n"},
			}
			for i, input := range inputs {
				if mask&(1<<i) != 0 {
					s.write(input.path, input.content, 0o640)
				}
			}
			plan, err := Plan(context.Background(), s.options("machine", "https://example.invalid/dots.git"))
			s.Require().NoError(err)
			_, err = s.apply(plan)
			s.Require().NoError(err)
			for i, input := range inputs {
				content, readErr := os.ReadFile(input.path)
				s.Require().NoError(readErr)
				if mask&(1<<i) != 0 {
					s.Equal(input.content, string(content))
				}
			}
			repeated, err := Plan(context.Background(), Options{})
			s.Require().NoError(err)
			s.Empty(repeated.Effects())
		})
	}
}

func (s *PreparationSuite) TestAllApprovalsPrecedeWrites() {
	configurations := filepath.Join(s.data, "configurations.toml")
	s.write(configurations, "[[configurations]]\nname = 'old'\napps = []\n"+
		"[[configurations]]\nname = 'new'\napps = []\n", 0o600)
	selection := filepath.Join(s.xdg, "dotm", "config.toml")
	s.write(selection, "configuration = 'old'\n", 0o600)
	plan, err := Plan(context.Background(), s.options("new", "https://example.invalid/dots.git"))
	s.Require().NoError(err)
	s.Require().Len(plan.Conflicts(), 2)
	calls := 0
	result, err := plan.Apply(context.Background(), func(_ context.Context, _ Conflict) (bool, error) {
		calls++
		s.NoFileExists(filepath.Join(s.data, "catalog.toml"))
		content, readErr := os.ReadFile(selection)
		s.Require().NoError(readErr)
		s.Equal("configuration = 'old'\n", string(content))
		return calls == 1, nil
	})
	s.Require().Error(err)
	s.Equal(2, calls)
	s.Empty(result.Completed)
	content, err := os.ReadFile(configurations)
	s.Require().NoError(err)
	s.NotContains(string(content), "repository")
}

func (s *PreparationSuite) TestInvalidLocalInputsCannotBeOverridden() {
	selection := filepath.Join(s.xdg, "dotm", "config.toml")
	for _, content := range []string{
		"configuration = 'invalid name'\n", "configuration = 42\n", "unknown = 'machine'\n", "[broken\n",
	} {
		s.write(selection, content, 0o600)
		_, err := Plan(context.Background(), s.options("machine", "https://example.invalid/dots.git"))
		s.Error(err)
		s.NoFileExists(filepath.Join(s.data, "catalog.toml"))
	}
	s.write(filepath.Join(s.data, "configurations.toml"), "[[configurations]]\nname = 'machine'\napps = []\n", 0o600)
	s.write(selection, "configuration = 'missing'\n", 0o600)
	_, err := Plan(context.Background(), s.options("machine", "https://example.invalid/dots.git"))
	s.Error(err)
}

func (s *PreparationSuite) TestMissingRepositoryRequiresExplicitInput() {
	_, err := Plan(context.Background(), s.options("machine", ""))
	s.Require().Error(err)
	s.Contains(err.Error(), "--repository")
	s.NoDirExists(s.data)
}

func (s *PreparationSuite) TestInferredRepositoryIsReadOnlyAndRechecked() {
	source := filepath.Join(s.home, "data", "chezmoi")
	s.T().Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	s.T().Setenv("GIT_CONFIG_NOSYSTEM", "1")
	run := func(args ...string) {
		cmd := exec.CommandContext(context.Background(), "git", args...)
		output, err := cmd.CombinedOutput()
		s.Require().NoError(err, "%s", output)
	}
	run("init", "--template=", "-b", "main", source)
	run("-C", source, "config", "user.name", "Preparation Test")
	run("-C", source, "config", "user.email", "test@example.invalid")
	tracked := filepath.Join(source, "tracked")
	s.write(tracked, "unchanged\n", 0o600)
	run("-C", source, "add", "tracked")
	run("-C", source, "-c", "commit.gpgsign=false", "commit", "-m", "fixture")
	repository := filepath.Join(s.home, "remote-not-contacted.git")
	run("-C", source, "remote", "add", "origin", repository)
	index := filepath.Join(source, ".git", "index")
	before, err := os.ReadFile(index)
	s.Require().NoError(err)
	stamp := time.Unix(1234567890, 0)
	s.Require().NoError(os.Chtimes(tracked, stamp, stamp))
	plan, err := Plan(context.Background(), s.options("machine", ""))
	s.Require().NoError(err)
	s.Equal(repository, plan.Repository)
	after, err := os.ReadFile(index)
	s.Require().NoError(err)
	s.Equal(before, after, "passive Git inspection must not refresh the index")
	run("-C", source, "remote", "set-url", "origin", filepath.Join(s.home, "changed.git"))
	result, err := plan.Apply(context.Background(), nil)
	s.Error(err)
	s.Empty(result.Completed)
	s.NoDirExists(s.data)
}
