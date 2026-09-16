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
		"post":           "printf 'post\\n' >> hook.log",
	} {
		s.Require().NoError(os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0755))
	}
	data := manifest.Data{CatalogDir: dir, Apps: map[string]manifest.Application{
		"dependency": {ID: "dependency", Packages: []manifest.Package{{Attr: "which"}}, PreHook: "dependency-pre", PostHook: "post"},
		"parent":     {ID: "parent", Dependencies: []string{"dependency"}, Packages: []manifest.Package{{Attr: "hello"}}, PreHook: "parent-pre", PostHook: "post"},
	}}
	desired, err := data.Resolve([]string{"parent"})
	s.Require().NoError(err)
	return dir, desired
}

func (s *HookOperationSuite) TestPreFailureStopsConsumersAndAllNixMutation() {
	dir, desired := s.fixture("printf 'dependency\\n' >> hook.log\nexit 7")
	report, err := Install(context.Background(), desired, io.Discard)
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

func (s *HookOperationSuite) TestPackageFailureSuppressesEveryPostHook() {
	dir, desired := s.fixture("printf 'dependency\\n' >> hook.log")
	report, err := Install(context.Background(), desired, io.Discard)
	var command *exec.Error
	s.Require().True(errors.As(err, &command))
	s.ErrorIs(command.Err, exec.ErrNotFound)
	log, readErr := os.ReadFile(filepath.Join(dir, "hook.log"))
	s.Require().NoError(readErr)
	s.Equal("dependency\nparent\n", string(log))
	_, statErr := os.Stat(filepath.Join(dir, "state", "dotm", "source", "flake.nix"))
	s.NoError(statErr, "source writing precedes the failed native lock command")
	s.Require().Len(report.Applications, 2)
	s.Equal("missing", report.Applications[0].State)
	s.Equal("missing", report.Applications[1].State)
}
