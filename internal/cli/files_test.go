package cli_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dotm/internal/chezmoi"
	"dotm/internal/cli"

	"github.com/stretchr/testify/suite"
)

type FilesCLISuite struct {
	suite.Suite
	base, dataDir, configHome, source, remote, home, policyPath string
}

func TestFilesCLISuite(t *testing.T) {
	if _, err := exec.LookPath("chezmoi"); err != nil {
		t.Skip("chezmoi is required for real files CLI tests")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required for real files CLI tests")
	}
	suite.Run(t, new(FilesCLISuite))
}

func (s *FilesCLISuite) SetupTest() {
	s.base = s.T().TempDir()
	s.dataDir = filepath.Join(s.base, "manifests")
	s.configHome = filepath.Join(s.base, "config")
	s.home = filepath.Join(s.base, "home")
	s.source = filepath.Join(s.base, "data", "chezmoi")
	s.remote = filepath.Join(s.base, "remote.git")
	for _, dir := range []string{
		s.dataDir, filepath.Join(s.configHome, "dotm"),
		filepath.Join(s.configHome, "chezmoi"), s.home, s.source,
	} {
		s.Require().NoError(os.MkdirAll(dir, 0o700))
	}
	for key, value := range map[string]string{
		"HOME": s.home, "XDG_CONFIG_HOME": s.configHome,
		"XDG_CONFIG_DIRS": filepath.Join(s.base, "system-config"),
		"XDG_DATA_HOME":   filepath.Join(s.base, "data"),
		"XDG_STATE_HOME":  filepath.Join(s.base, "state"),
		"XDG_CACHE_HOME":  filepath.Join(s.base, "cache"),
	} {
		s.T().Setenv(key, value)
	}
	s.git("init", "--bare", "--quiet", s.remote)
	s.git("init", "--quiet", s.source)
	s.git("-C", s.source, "remote", "add", "origin", s.remote)
	template, err := os.ReadFile(filepath.Join("..", "..", "examples", "chezmoi-policy", ".chezmoiignore"))
	s.Require().NoError(err)
	s.write(filepath.Join(s.source, ".chezmoiignore"), string(template))
	s.git("-C", s.source, "add", "--all")
	s.git("-C", s.source, "-c", "user.name=Files Test", "-c", "user.email=files@example.invalid",
		"commit", "--quiet", "-m", "fixture")
	s.git("-C", s.source, "push", "--quiet", "-u", "origin", "HEAD")
	chezmoiConfig := filepath.Join(s.configHome, "chezmoi", "chezmoi.toml")
	path, err := chezmoi.PolicyPath(chezmoi.Policy{
		ConfigFile: chezmoiConfig, SourceDir: s.source, DestDir: s.home,
	})
	s.Require().NoError(err)
	s.policyPath = path
	s.write(chezmoiConfig, "sourceDir = "+strconvQuote(s.source)+"\ndestDir = "+strconvQuote(s.home)+
		"\n[data.dotm]\npolicyFile = "+strconvQuote(path)+"\n")
	s.write(filepath.Join(s.dataDir, "catalog.toml"), "")
	s.write(filepath.Join(s.dataDir, "configurations.toml"), configurationManifest(s.remote, "[]"))
	s.write(filepath.Join(s.configHome, "dotm", "config.toml"), "configuration = \"work\"\n")
}

func (s *FilesCLISuite) write(path, contents string) {
	s.T().Helper()
	//nolint:gosec // G703: fixture paths are confined to this test's private temporary directory.
	s.Require().NoError(os.MkdirAll(filepath.Dir(path), 0o700))
	//nolint:gosec // G703: fixture paths are confined to this test's private temporary directory.
	s.Require().NoError(os.WriteFile(path, []byte(contents), 0o600))
}

func (s *FilesCLISuite) git(args ...string) {
	s.T().Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	output, err := cmd.CombinedOutput()
	s.Require().NoError(err, "%s: %s", strings.Join(args, " "), output)
}

func (s *FilesCLISuite) run(args ...string) (string, error) {
	s.T().Helper()
	args = append([]string{"--data-dir", s.dataDir}, args...)
	var output bytes.Buffer
	err := cli.Run(args, strings.NewReader(""), &output, &output)
	return output.String(), err
}

func (s *FilesCLISuite) TestRefreshRepairsStalePolicyAndLintIsReadOnly() {
	output, err := s.run("files", "refresh", "--repository-id", "dotm-example")
	s.Require().NoError(err, output)
	s.Contains(output, "Publication: refreshed and verified")
	published, err := os.ReadFile(s.policyPath)
	s.Require().NoError(err)

	lintOutput, err := s.run("files", "lint", "--repository-id", "dotm-example")
	s.Require().NoError(err, lintOutput)
	afterLint, err := os.ReadFile(s.policyPath)
	s.Require().NoError(err)
	s.Equal(published, afterLint)
	_, err = os.Stat(filepath.Join(s.source, ".git", "FETCH_HEAD"))
	s.ErrorIs(err, os.ErrNotExist, "local refresh and lint must not fetch")

	s.write(s.policyPath, `{}`)
	_, err = s.run("files", "lint", "--repository-id", "dotm-example")
	s.Require().Error(err)
	s.Equal(1, cli.ExitCode(err))
	output, err = s.run("files", "refresh", "--repository-id", "dotm-example")
	s.Require().NoError(err, output)
	fixed, err := os.ReadFile(s.policyPath)
	s.Require().NoError(err)
	s.NotEqual([]byte(`{}`), fixed)
}

func (s *FilesCLISuite) TestExplicitSourceUpdateRefusesDirtyCheckout() {
	s.write(filepath.Join(s.source, "dirty"), "uncommitted")
	output, err := s.run("files", "refresh", "--repository-id", "dotm-example", "--update-source")
	s.Require().Error(err, output)
	s.Equal(1, cli.ExitCode(err))
	contents, readErr := os.ReadFile(filepath.Join(s.source, "dirty"))
	s.Require().NoError(readErr)
	s.Equal("uncommitted", string(contents))
}

func (s *FilesCLISuite) TestRefreshBlocksPendingPackageRecoveryWithoutNix() {
	journal := filepath.Join(os.Getenv("XDG_STATE_HOME"), "dotm", "update-recovery.json")
	s.write(journal, `{}`)
	output, err := s.run("files", "refresh", "--repository-id", "dotm-example")
	s.Require().Error(err, output)
	s.Equal(3, cli.ExitCode(err))
	s.Contains(err.Error(), "recover it before files refresh")
	_, statErr := os.Stat(filepath.Join(s.source, ".git", "FETCH_HEAD"))
	s.ErrorIs(statErr, os.ErrNotExist)
}

func (s *FilesCLISuite) TestRefreshRejectsNativeNegationBeforePublishing() {
	// An unmanaged selected path is generated as an exclusion; a native root rule
	// that explicitly re-includes that destination must block publication.
	s.write(filepath.Join(s.source, ".chezmoiignore"), "!.config/cache\n")
	s.write(filepath.Join(s.source, "dot_config", "cache"), "managed cache\n")
	s.git("-C", s.source, "add", ".chezmoiignore", "dot_config/cache")
	s.git("-C", s.source, "-c", "user.name=Files Test", "-c", "user.email=files@example.invalid",
		"commit", "--quiet", "-m", "native-negation")
	s.git("-C", s.source, "push", "--quiet")
	s.write(filepath.Join(s.dataDir, "catalog.toml"),
		"[cache]\nunmanaged = [\"~/.config/cache\"]\n")
	s.write(filepath.Join(s.dataDir, "configurations.toml"), configurationManifest(s.remote, "[\"cache\"]"))
	_, removeErr := os.Stat(s.policyPath)
	s.ErrorIs(removeErr, os.ErrNotExist)
	output, err := s.run("files", "refresh", "--repository-id", "dotm-example")
	s.Require().Error(err, output)
	s.Equal(1, cli.ExitCode(err))
	_, statErr := os.Stat(s.policyPath)
	s.ErrorIs(statErr, os.ErrNotExist)
}

func strconvQuote(value string) string {
	return fmt.Sprintf("%q", value)
}

func configurationManifest(repository, applications string) string {
	return "[[configurations]]\nname = \"work\"\napps = " + applications +
		"\n[configurations.configs]\nbackend = \"chezmoi\"\nrepository = " +
		strconvQuote(repository) + "\n"
}

func (s *FilesCLISuite) TestFilesCommandsRequireRepositoryID() {
	for _, args := range [][]string{{"files", "lint"}, {"files", "refresh"}, {"files", "status"}} {
		_, err := s.run(args...)
		s.Require().Error(err)
		s.Equal(3, cli.ExitCode(err))
		s.NoFileExists(s.policyPath)
	}
}

func (s *FilesCLISuite) TestWarningOnlyDeclarationsStillRefreshPolicy() {
	s.write(filepath.Join(s.dataDir, "catalog.toml"),
		"[cache]\nunmanaged = [\"~/.config/cache\", \"~/.config/cache/child\"]\n")
	s.write(filepath.Join(s.dataDir, "configurations.toml"), configurationManifest(s.remote, "[\"cache\"]"))
	output, err := s.run("files", "refresh", "--repository-id", "dotm-example")
	s.Require().NoError(err, output)
	s.Contains(output, "warning:")
	s.Contains(output, "Publication: refreshed and verified")
}

func (s *FilesCLISuite) TestManifestInputAndHostIOHaveDistinctExitCodes() {
	catalog := filepath.Join(s.dataDir, "catalog.toml")
	s.Require().NoError(os.Remove(catalog))
	output, err := s.run("files", "refresh", "--repository-id", "dotm-example")
	s.Require().Error(err, output)
	s.Equal(1, cli.ExitCode(err), "missing checked input is a configuration failure")

	s.Require().NoError(os.Mkdir(catalog, 0o700))
	output, err = s.run("files", "refresh", "--repository-id", "dotm-example")
	s.Require().Error(err, output)
	s.Equal(3, cli.ExitCode(err), "catalog read I/O failures are internal")
}
