package installed

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"dotm/internal/manifest"

	"github.com/stretchr/testify/suite"
)

type HookOperationSuite struct{ suite.Suite }

func TestHookOperationSuite(t *testing.T) { suite.Run(t, new(HookOperationSuite)) }

func (s *HookOperationSuite) fixture(preBody string) (string, manifest.Resolved) {
	s.T().Helper()
	dir := s.T().TempDir()
	s.T().Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	s.T().Setenv("PATH", "")
	for name, body := range map[string]string{
		"dependency-pre": preBody,
		"parent-pre":     "printf 'parent\\n' >> hook.log",
		"pre-cfg":        "printf 'pre-cfg\\n' >> hook.log",
		"post-cfg":       "printf 'post-cfg\\n' >> hook.log",
	} {
		// #nosec G306 -- Hook fixtures need execution permission, restricted to the current user.
		s.Require().NoError(os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o700))
	}
	data := manifest.Data{CatalogDir: dir, Apps: map[string]manifest.Application{
		"dependency": {
			ID: "dependency", Packages: []manifest.Package{{Attr: "which"}},
			PreHook: "dependency-pre", PreCfgHook: "pre-cfg", PostCfgHook: "post-cfg",
		},
		"parent": {
			ID: "parent", Dependencies: []string{"dependency"},
			Packages: []manifest.Package{{Attr: "hello"}}, PreHook: "parent-pre",
			PreCfgHook: "pre-cfg", PostCfgHook: "post-cfg",
		},
	}}
	desired, err := data.Resolve([]string{"parent"})
	s.Require().NoError(err)
	return dir, desired
}

func (s *HookOperationSuite) TestPreFailureStopsConsumersAndAllNixMutation() {
	dir, desired := s.fixture("printf 'dependency\\n' >> hook.log\nexit 7")
	report, err := Install(context.Background(), desired, io.Discard, ConfigurationOptions{})
	var exit *exec.ExitError
	s.Require().True(errors.As(err, &exit))
	s.Equal(7, exit.ExitCode())
	log, readErr := os.ReadFile(filepath.Join(dir, "hook.log"))
	s.Require().NoError(readErr)
	s.Equal("dependency\n", string(log))
	_, statErr := os.Lstat(filepath.Join(dir, "state"))
	s.ErrorIs(statErr, os.ErrNotExist, "no source, lock, or profile may be initialized")
	s.Require().Len(report.Applications, 2)
	s.Equal("missing", report.Applications[0].State)
	s.Equal("missing", report.Applications[1].State)
}

func (s *HookOperationSuite) TestPackageFailureDoesNotRunConfigurationHooks() {
	dir, desired := s.fixture("printf 'dependency\\n' >> hook.log")
	report, err := Install(context.Background(), desired, io.Discard, ConfigurationOptions{})
	var command *exec.Error
	s.Require().True(errors.As(err, &command))
	s.ErrorIs(command.Err, exec.ErrNotFound)
	log, readErr := os.ReadFile(filepath.Join(dir, "hook.log"))
	s.Require().NoError(readErr)
	s.Equal("dependency\nparent\n", string(log))
	s.NotContains(string(log), "pre-cfg")
	s.NotContains(string(log), "post-cfg")
	s.Require().Len(report.Applications, 2)
	s.Equal("missing", report.Applications[0].State)
	s.Equal("missing", report.Applications[1].State)
}

func (s *HookOperationSuite) TestPackageFreeInstallRunsConfigurationHooks() {
	dir := s.T().TempDir()
	s.T().Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	hook := filepath.Join(dir, "config-hook")
	// #nosec G306 -- Executable fixture is private and only verifies deferred execution.
	s.Require().NoError(os.WriteFile(hook, []byte("#!/bin/sh\nprintf 'unexpected\\n' >> hook.log\n"), 0o700))
	data := manifest.Data{CatalogDir: dir, Apps: map[string]manifest.Application{
		"config": {ID: "config", PreCfgHook: filepath.Base(hook), PostCfgHook: filepath.Base(hook)},
	}}
	desired, err := data.Resolve([]string{"config"})
	s.Require().NoError(err)
	report, err := Install(context.Background(), desired, io.Discard, ConfigurationOptions{})
	s.Require().NoError(err)
	s.Equal("package-free", report.Applications[0].State)
	s.True(report.Applications[0].Ready)
	log, err := os.ReadFile(filepath.Join(dir, "hook.log"))
	s.Require().NoError(err)
	s.Equal("unexpected\nunexpected\n", string(log))
}
