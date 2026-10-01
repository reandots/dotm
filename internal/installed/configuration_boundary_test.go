package installed

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"dotm/internal/manifest"

	"github.com/stretchr/testify/suite"
)

type ConfigurationBoundarySuite struct{ suite.Suite }

func TestConfigurationBoundarySuite(t *testing.T) {
	suite.Run(t, new(ConfigurationBoundarySuite))
}

func (s *ConfigurationBoundarySuite) fixture() string {
	s.T().Helper()
	dir := s.T().TempDir()
	s.T().Setenv("HOME", dir)
	s.T().Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	s.T().Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	s.T().Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	s.T().Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	return dir
}

func (s *ConfigurationBoundarySuite) TestSourceFailureBlocksHookOnlyPostAndKeepsPreEffects() {
	dir := s.fixture()
	s.T().Setenv("PATH", "")
	// #nosec G306 -- Hooks are executable fixtures, private to the test owner.
	s.Require().NoError(os.WriteFile(filepath.Join(dir, "pre"), []byte(
		"#!/bin/sh\nprintf prepared > prepared\n"), 0o700))
	// #nosec G306 -- Hooks are executable fixtures, private to the test owner.
	s.Require().NoError(os.WriteFile(filepath.Join(dir, "post"), []byte(
		"#!/bin/sh\nprintf unexpected > post-ran\n"), 0o700))
	data := manifest.Data{CatalogDir: dir, Apps: map[string]manifest.Application{
		"config": {ID: "config", Configs: []string{filepath.Join(dir, "target")}},
		"hooks":  {ID: "hooks", PreCfgHook: "pre", PostCfgHook: "post"},
	}}
	desired, err := data.Resolve([]string{"config", "hooks"})
	s.Require().NoError(err)
	report, err := Configure(context.Background(), desired, io.Discard, ConfigurationOptions{
		Source: &manifest.ConfigurationSource{Backend: "chezmoi", Repository: filepath.Join(dir, "absent")},
	})
	var outcome *OperationError
	s.Require().ErrorAs(err, &outcome)
	s.Equal(SeverityConfiguration, outcome.Severity)
	s.Equal(StageFailed, report.Configuration.SourceStage.Status)
	s.Equal(StageSuccess, report.Configuration.Applications[1].Pre.Status)
	s.Equal(StageSkipped, report.Configuration.Applications[1].Post.Status)
	s.FileExists(filepath.Join(dir, "prepared"))
	s.NoFileExists(filepath.Join(dir, "post-ran"))
	s.NoDirExists(filepath.Join(dir, "state"), "source errors must not create Nix state")
}

type failedConfigurationOutput struct{ err error }

func (w failedConfigurationOutput) Write([]byte) (int, error) { return 0, w.err }

func (s *ConfigurationBoundarySuite) TestOutputFailureOutranksHookFailure() {
	dir := s.fixture()
	// #nosec G306 -- Hooks are executable fixtures, private to the test owner.
	s.Require().NoError(os.WriteFile(filepath.Join(dir, "hook"), []byte("#!/bin/sh\nexit 7\n"), 0o700))
	desired := manifest.Resolved{CatalogDir: dir, Applications: []manifest.Application{
		{ID: "app", PreCfgHook: "hook"},
	}}
	writeErr := errors.New("report stream closed")
	report, err := Configure(context.Background(), desired, failedConfigurationOutput{err: writeErr},
		ConfigurationOptions{})
	var outcome *OperationError
	s.Require().ErrorAs(err, &outcome)
	s.Equal(SeverityInternal, outcome.Severity)
	s.ErrorIs(err, writeErr)
	s.Equal(SeverityInternal, report.Configuration.Severity)
}

func (s *ConfigurationBoundarySuite) TestInspectionFailureOutranksHookFailure() {
	dir := s.fixture()
	s.T().Setenv("PATH", "/usr/bin:/bin")
	// This hook models arbitrary trusted user code corrupting package state.
	// #nosec G306 -- Hooks are executable fixtures, private to the test owner.
	s.Require().NoError(os.WriteFile(filepath.Join(dir, "hook"), []byte(
		"#!/bin/sh\nmkdir -p \"$XDG_STATE_HOME/nix/profile\"\nexit 7\n"), 0o700))
	desired := manifest.Resolved{CatalogDir: dir, Applications: []manifest.Application{
		{ID: "app", PreCfgHook: "hook"},
	}}
	report, err := Configure(context.Background(), desired, io.Discard, ConfigurationOptions{})
	var outcome *OperationError
	s.Require().ErrorAs(err, &outcome)
	s.Equal(SeverityInternal, outcome.Severity)
	s.Empty(report.Profile, "failed inspection must not leave an assumed-success package report")
	s.Equal(StageFailed, report.Configuration.Applications[0].Pre.Status)
	s.ErrorContains(err, "inspect after configuration hooks")
}
