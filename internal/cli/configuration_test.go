package cli_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dotm/internal/cli"
	"dotm/internal/installed"

	"github.com/stretchr/testify/suite"
)

type ConfigurationCLISuite struct {
	suite.Suite
	base, dataDir, configDir string
}

func TestConfigurationCLISuite(t *testing.T) { suite.Run(t, new(ConfigurationCLISuite)) }

func (s *ConfigurationCLISuite) SetupTest() {
	s.base = s.T().TempDir()
	s.dataDir = filepath.Join(s.base, "data")
	s.configDir = filepath.Join(s.base, "xdg", "dotm")
	s.Require().NoError(os.MkdirAll(s.dataDir, 0o700))
	s.Require().NoError(os.MkdirAll(s.configDir, 0o700))
	s.T().Setenv("HOME", filepath.Join(s.base, "home"))
	s.T().Setenv("XDG_CONFIG_HOME", filepath.Join(s.base, "xdg"))
	s.T().Setenv("XDG_DATA_HOME", filepath.Join(s.base, "xdg-data"))
	s.T().Setenv("XDG_STATE_HOME", filepath.Join(s.base, "xdg-state"))
	s.T().Setenv("XDG_CACHE_HOME", filepath.Join(s.base, "xdg-cache"))
	s.T().Setenv("PATH", "")
}

func (s *ConfigurationCLISuite) manifests(catalog, configurations, selection string) {
	s.T().Helper()
	s.Require().NoError(os.WriteFile(filepath.Join(s.dataDir, "catalog.toml"), []byte(catalog), 0o600))
	s.Require().NoError(os.WriteFile(filepath.Join(s.dataDir, "configurations.toml"), []byte(configurations), 0o600))
	if selection != "" {
		s.Require().NoError(os.WriteFile(filepath.Join(s.configDir, "config.toml"), []byte(selection), 0o600))
	}
}

func (s *ConfigurationCLISuite) run(args ...string) (string, string, error) {
	s.T().Helper()
	var out, stderr bytes.Buffer
	args = append([]string{"--data-dir", s.dataDir}, args...)
	err := cli.Run(args, strings.NewReader(""), &out, &stderr)
	return out.String(), stderr.String(), err
}

func (s *ConfigurationCLISuite) TestUpdateAndConfigurationCommandParsing() {
	s.manifests("", "[[configurations]]\nname='empty'\napps=[]\n", "configuration='empty'\n")
	for _, args := range [][]string{{"update"}, {"update", "cfg"}} {
		_, _, err := s.run(args...)
		s.NoError(err)
	}
	for _, args := range [][]string{{"-i", "update"}, {"update", "-u"}, {"update", "cfg", "-i"}} {
		_, _, err := s.run(args...)
		s.Error(err, args)
	}
}

func (s *ConfigurationCLISuite) TestCfgLintValidatesExplicitSourceReadOnly() {
	source := filepath.Join(s.base, "source")
	s.Require().NoError(os.MkdirAll(source, 0o700))
	_, _, err := s.run("cfg", "lint", "--source", source)
	s.NoError(err)
	entries, readErr := os.ReadDir(source)
	s.Require().NoError(readErr)
	s.Empty(entries)
	// Lint does not require manifests or a local selection.
	_, statErr := os.Stat(filepath.Join(s.dataDir, "catalog.toml"))
	s.ErrorIs(statErr, os.ErrNotExist)
	policyFile := filepath.Join(source, ".chezmoiroot")
	s.Require().NoError(os.WriteFile(policyFile, []byte("../outside\n"), 0o600))
	out, stderr, lintErr := s.run("cfg", "lint", "--source", source)
	s.Require().Error(lintErr)
	s.Equal(1, cli.ExitCode(lintErr))
	s.Equal(1, strings.Count(out+stderr+lintErr.Error(), "source-syntax"))
	s.Contains(lintErr.Error(), policyFile)
	_, _, err = s.run("cfg", "lint", "--source", "relative")
	s.Error(err)
	s.Equal(3, cli.ExitCode(err))
}

func (s *ConfigurationCLISuite) TestExitCodeRetainsMaximumAcrossWrappedJoinedCauses() {
	configuration := &installed.OperationError{Severity: installed.SeverityConfiguration, Err: os.ErrInvalid}
	installation := &installed.OperationError{Severity: installed.SeverityInstallation, Err: os.ErrInvalid}
	s.Equal(0, cli.ExitCode(nil))
	s.Equal(2, cli.ExitCode(errors.Join(fmt.Errorf("update: %w", configuration), installation)))
	s.Equal(2, cli.ExitCode(errors.Join(installation, fmt.Errorf("install: %w", configuration))))
	s.Equal(3, cli.ExitCode(errors.Join(configuration, os.ErrInvalid)))
}

func (s *ConfigurationCLISuite) TestConfigOnlyRunsHooksWithoutPackageTool() {
	hook := filepath.Join(s.dataDir, "hook.sh")
	log := filepath.Join(s.base, "hook.log")
	// #nosec G306 -- Private executable fixture used only by this test.
	s.Require().NoError(os.WriteFile(hook, []byte("#!/bin/sh\necho called >> \"$HOOK_LOG\"\n"), 0o700))
	s.T().Setenv("HOOK_LOG", log)
	s.manifests("[app]\nnix=[]\npre_hook='hook.sh'\npre_cfg_hook='hook.sh'\npost_cfg_hook='hook.sh'\n",
		"[[configurations]]\nname='empty'\napps=['app']\n", "configuration='empty'\n")
	_, _, err := s.run("update", "cfg")
	s.NoError(err)
	data, readErr := os.ReadFile(log)
	s.Require().NoError(readErr)
	s.Equal("called\ncalled\n", string(data))
}

func (s *ConfigurationCLISuite) TestCombinedUpdateContinuesAfterConfigurationHookFailure() {
	log := filepath.Join(s.base, "hook.log")
	hook := filepath.Join(s.dataDir, "once.sh")
	marker := filepath.Join(s.base, "first-call")
	body := "#!/bin/sh\necho called >> \"$HOOK_LOG\"\n" +
		"if [ ! -e \"$HOOK_MARKER\" ]; then : > \"$HOOK_MARKER\"; exit 1; fi\n"
	// #nosec G306 -- Private executable fixture used only by this test.
	s.Require().NoError(os.WriteFile(hook, []byte(body), 0o700))
	s.T().Setenv("HOOK_LOG", log)
	s.T().Setenv("HOOK_MARKER", marker)
	s.manifests("[app]\nnix=[]\npre_cfg_hook='once.sh'\n",
		"[[configurations]]\nname='empty'\napps=['app']\n", "configuration='empty'\n")
	_, stderr, err := s.run("-ui")
	s.Error(err)
	s.Equal(1, cli.ExitCode(err))
	data, readErr := os.ReadFile(log)
	s.Require().NoError(readErr)
	s.Equal("called\ncalled\n", string(data))
	s.Contains(stderr, "app")
}

func (s *ConfigurationCLISuite) TestCombinedConfigFailureThenInstallationFailureRetainsHigherSeverity() {
	log := filepath.Join(s.base, "hook.log")
	marker := filepath.Join(s.base, "first-config")
	configHook := filepath.Join(s.dataDir, "config.sh")
	packageHook := filepath.Join(s.dataDir, "package.sh")
	// #nosec G306 -- Private executable fixtures used only by this test.
	s.Require().NoError(os.WriteFile(configHook, []byte("#!/bin/sh\necho config >> \"$HOOK_LOG\"\n"+
		"if [ ! -e \"$HOOK_MARKER\" ]; then : > \"$HOOK_MARKER\"; exit 1; fi\n"), 0o700))
	// #nosec G306 -- Private executable fixture used only by this test.
	s.Require().NoError(os.WriteFile(packageHook, []byte("#!/bin/sh\necho package >> \"$HOOK_LOG\"\n"+
		"if [ -e \"$HOOK_MARKER\" ]; then exit 1; fi\n"), 0o700))
	s.T().Setenv("HOOK_LOG", log)
	s.T().Setenv("HOOK_MARKER", marker)
	s.manifests("[app]\nnix=[]\npre_cfg_hook='config.sh'\npre_hook='package.sh'\n",
		"[[configurations]]\nname='empty'\napps=['app']\n", "configuration='empty'\n")
	_, _, err := s.run("-ui")
	s.Error(err)
	s.Equal(2, cli.ExitCode(err))
	data, readErr := os.ReadFile(log)
	s.Require().NoError(readErr)
	s.Equal("config\npackage\n", string(data))
}

func (s *ConfigurationCLISuite) TestConfigFailureReportsDependencySkip() {
	hook := filepath.Join(s.dataDir, "fail.sh")
	// #nosec G306 -- Private executable fixture used only by this test.
	s.Require().NoError(os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o700))
	s.manifests("[dependency]\nnix=[]\npre_cfg_hook='fail.sh'\n[consumer]\ndependencies=['dependency']\nnix=[]\n",
		"[[configurations]]\nname='empty'\napps=['consumer']\n", "configuration='empty'\n")
	out, _, err := s.run("update", "cfg")
	s.Error(err)
	s.Contains(out, "consumer: \n    pre_cfg_hook: skipped; blocked by dependency")
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, os.ErrPermission }

func (s *ConfigurationCLISuite) TestFinalReportWriterFailureIsInternal() {
	s.manifests("[app]\nnix=[]\n", "[[configurations]]\nname='empty'\napps=['app']\n", "configuration='empty'\n")
	err := cli.Run([]string{"--data-dir", s.dataDir, "update", "cfg"},
		strings.NewReader(""), failingWriter{}, io.Discard)
	s.Error(err)
	s.Equal(3, cli.ExitCode(err))
}
