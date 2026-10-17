package cli_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dotm/internal/cli"
	"dotm/internal/manifest"

	"github.com/stretchr/testify/suite"
)

type InitCLISuite struct {
	suite.Suite
	root, home, config, data, source, remote, state string
}

func TestInitCLISuite(t *testing.T) {
	for _, executable := range []string{"git", "chezmoi"} {
		if _, err := exec.LookPath(executable); err != nil {
			t.Skip(executable + " is required for init integration tests")
		}
	}
	suite.Run(t, new(InitCLISuite))
}

func (s *InitCLISuite) SetupTest() {
	s.root = s.T().TempDir()
	s.home = filepath.Join(s.root, "home")
	s.config = filepath.Join(s.root, "config")
	s.data = filepath.Join(s.root, "manifests")
	s.source = filepath.Join(s.root, "data", "chezmoi")
	s.remote = filepath.Join(s.root, "remote")
	s.state = filepath.Join(s.root, "state")
	for key, value := range map[string]string{
		"HOME": s.home, "XDG_CONFIG_HOME": s.config,
		"XDG_DATA_HOME": filepath.Join(s.root, "data"), "XDG_STATE_HOME": s.state,
		"XDG_CACHE_HOME":    filepath.Join(s.root, "cache"),
		"XDG_CONFIG_DIRS":   filepath.Join(s.root, "system-config"),
		"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_SYSTEM": "/dev/null",
	} {
		s.T().Setenv(key, value)
	}
	s.Require().NoError(os.MkdirAll(s.home, 0o700))
	s.Require().NoError(os.MkdirAll(s.remote, 0o700))
	s.git("init", "--quiet", s.remote)
	s.write(filepath.Join(s.remote, ".chezmoiignore"), "# native rules\nnative-ignored\n")
	s.write(filepath.Join(s.remote, "dot_init-destination"), "must not be applied\n")
	s.git("-C", s.remote, "add", "--all")
	s.git("-C", s.remote, "-c", "user.name=Init Test", "-c", "user.email=init@example.invalid",
		"commit", "--quiet", "-m", "fixture")
}

func (s *InitCLISuite) write(path, contents string) {
	s.T().Helper()
	s.Require().NoError(os.MkdirAll(filepath.Dir(path), 0o700))
	// #nosec G703 -- Callers supply fixture paths under this test's private temporary root.
	s.Require().NoError(os.WriteFile(path, []byte(contents), 0o600))
}

func (s *InitCLISuite) git(args ...string) string {
	s.T().Helper()
	output, err := exec.CommandContext(context.Background(), "git", args...).CombinedOutput()
	s.Require().NoError(err, "%s", output)
	return string(output)
}

func (s *InitCLISuite) run(input string, args ...string) (string, error) {
	s.T().Helper()
	var output bytes.Buffer
	err := cli.Run(append([]string{"--data-dir", s.data}, args...), strings.NewReader(input), &output, &output)
	return output.String(), err
}

func (s *InitCLISuite) TestSetupRepeatPreservesDirtySourceAndDoesNotDeploy() {
	output, err := s.run("", "init", "--configuration", "work", "--repository", s.remote)
	s.Require().NoError(err, output)
	s.FileExists(filepath.Join(s.state, "dotm", "chezmoi-policy.json"))
	s.NoFileExists(filepath.Join(s.home, ".init-destination"))
	s.NoDirExists(filepath.Join(s.state, "nix"))
	data, loadErr := manifest.Load(s.data)
	s.Require().NoError(loadErr)
	s.NotContains(data.Apps, "chezmoi", "external backend reuse must not edit the application catalog")
	selectionPath, selectionErr := manifest.SelectionPath()
	s.Require().NoError(selectionErr)
	selected, selectionErr := data.Selection(selectionPath)
	s.Require().NoError(selectionErr)
	s.Empty(selected.Apps, "external backend reuse must not add a managed application")
	s.Equal("chezmoi", selected.Backend)
	s.Equal(s.remote, selected.Repository)
	ignore := filepath.Join(s.source, ".chezmoiignore")
	first, err := os.ReadFile(ignore)
	s.Require().NoError(err)
	s.Contains(string(first), "{{/* dotm:policy begin */}}")
	s.Contains(string(first), "# native rules\nnative-ignored\n")
	policy := filepath.Join(s.state, "dotm", "chezmoi-policy.json")
	before, err := os.Stat(policy)
	s.Require().NoError(err)
	output, err = s.run("", "init")
	s.Require().NoError(err, output)
	after, err := os.Stat(policy)
	s.Require().NoError(err)
	s.True(os.SameFile(before, after), "identical policy must not be republished")
	head := s.git("-C", s.source, "rev-parse", "HEAD")
	s.write(filepath.Join(s.source, "untracked-local"), "keep me\n")
	// A remote advance must not be fetched or merged on repeated setup.
	s.write(filepath.Join(s.remote, "dot_remote-only"), "not fetched\n")
	s.git("-C", s.remote, "add", "--all")
	s.git("-C", s.remote, "-c", "user.name=Init Test", "-c", "user.email=init@example.invalid",
		"commit", "--quiet", "-m", "advance")
	output, err = s.run("", "init")
	s.Require().NoError(err, output)
	s.Equal(head, s.git("-C", s.source, "rev-parse", "HEAD"))
	s.NoFileExists(filepath.Join(s.source, "dot_remote-only"))
	s.FileExists(filepath.Join(s.source, "untracked-local"))
	repeated, err := os.ReadFile(ignore)
	s.Require().NoError(err)
	s.Equal(first, repeated)
}

func (s *InitCLISuite) TestApprovalsBeforeWritesAndNoYesBypass() {
	s.write(filepath.Join(s.data, "catalog.toml"), "")
	s.write(filepath.Join(s.data, "configurations.toml"), "[[configurations]]\nname='old'\napps=[]\n"+
		"backend='chezmoi'\nrepository="+strconvQuote(s.remote)+"\n"+
		"[[configurations]]\nname='work'\napps=[]\n"+
		"backend='chezmoi'\nrepository='old-repository'\n")
	selection := filepath.Join(s.config, "dotm", "config.toml")
	s.write(selection, "configuration='old'\n")
	for _, input := range []string{"no\n", "yes\n", "yes\nno\n"} {
		_, err := s.run(input, "init", "--configuration", "work", "--repository", s.remote)
		s.Require().Error(err)
		content, readErr := os.ReadFile(selection)
		s.Require().NoError(readErr)
		s.Equal("configuration='old'\n", string(content))
		s.NoFileExists(filepath.Join(s.config, "dotm", "files.toml"))
		s.NoDirExists(s.source)
	}
	_, err := s.run("yes\nyes\n", "--yes", "init", "--configuration", "work", "--repository", s.remote)
	s.Require().Error(err)
	output, err := s.run("yes\nyes\n", "init", "--configuration", "work", "--repository", s.remote)
	s.Require().NoError(err, output)
	content, err := os.ReadFile(selection)
	s.Require().NoError(err)
	selected, err := manifest.SelectionContents(selection, content)
	s.Require().NoError(err)
	s.Equal("work", selected)
}

func (s *InitCLISuite) TestInvalidInputAndPendingRecoveryNeverPrepareFiles() {
	s.write(filepath.Join(s.data, "catalog.toml"), "[broken")
	_, err := s.run("", "init", "--configuration", "work", "--repository", s.remote)
	s.Require().Error(err)
	s.NoDirExists(s.config)
	s.NoDirExists(s.source)
	s.Require().NoError(os.Remove(filepath.Join(s.data, "catalog.toml")))
	s.write(filepath.Join(s.state, "dotm", "update-recovery.json"), "pending")
	_, err = s.run("", "init", "--configuration", "work", "--repository", s.remote)
	s.Require().ErrorContains(err, "pending")
	s.NoFileExists(filepath.Join(s.data, "catalog.toml"))
	s.NoDirExists(s.config)
}

func (s *InitCLISuite) TestAcquisitionFailureRetainsPreparedFilesAndSafeRerun() {
	missing := filepath.Join(s.root, "missing-repository")
	output, err := s.run("", "init", "--configuration", "work", "--repository", missing)
	s.Require().ErrorContains(err, "source acquisition")
	s.Contains(output, filepath.Join(s.config, "dotm", "config.toml"))
	s.FileExists(filepath.Join(s.data, "catalog.toml"))
	s.NoFileExists(filepath.Join(s.state, "dotm", "chezmoi-policy.json"))
	output, err = s.run("yes\n", "init", "--repository", s.remote)
	s.Require().NoError(err, output)
}

func (s *InitCLISuite) TestInitDoesNotReplacePolicySymlink() {
	output, err := s.run("", "init", "--configuration", "work", "--repository", s.remote)
	s.Require().NoError(err, output)
	policy := filepath.Join(s.state, "dotm", "chezmoi-policy.json")
	content, err := os.ReadFile(policy)
	s.Require().NoError(err)
	target := filepath.Join(s.root, "user-policy.json")
	s.write(target, string(content))
	s.Require().NoError(os.Remove(policy))
	s.Require().NoError(os.Symlink(target, policy))
	_, err = s.run("", "init")
	s.Require().Error(err)
	link, err := os.Readlink(policy)
	s.Require().NoError(err)
	s.Equal(target, link)
	retained, err := os.ReadFile(target)
	s.Require().NoError(err)
	s.Equal(content, retained)
}
