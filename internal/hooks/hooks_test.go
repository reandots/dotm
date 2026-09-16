package hooks

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"dotm/internal/manifest"

	"github.com/stretchr/testify/suite"
)

type HooksSuite struct {
	suite.Suite
	root string
}

func TestHooksSuite(t *testing.T) { suite.Run(t, new(HooksSuite)) }

func (s *HooksSuite) SetupTest() {
	s.root = s.T().TempDir()
}

func (s *HooksSuite) executable(name, body string) string {
	path := filepath.Join(s.root, name)
	s.Require().NoError(os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0755))
	return path
}

func (s *HooksSuite) TestRunUsesCatalogDirectoryAndProfileFirstPathAndRoutesOutput() {
	bin := filepath.Join(s.root, "profile", "bin")
	s.Require().NoError(os.MkdirAll(bin, 0755))
	s.Require().NoError(os.WriteFile(filepath.Join(bin, "hook-tool"), []byte("#!/bin/sh\nprintf profile-tool\n"), 0755))
	s.T().Setenv("PATH", "/nonexistent")
	path := s.executable("hook", "printf '%s|' \"$PWD\"; hook-tool; printf '|hook-output'; printf '|hook-error' >&2")
	var output bytes.Buffer
	desired := manifest.Resolved{CatalogDir: s.root, Applications: []manifest.Application{{ID: "editor", PreHook: path}}}
	configured, err := Run(context.Background(), desired, Pre, filepath.Join(s.root, "profile"), &output)
	s.True(configured)
	s.Require().NoError(err)
	s.Contains(output.String(), s.root+"|profile-tool|hook-output|hook-error")
	s.Contains(output.String(), "application editor pre_hook: "+path)
}

func (s *HooksSuite) TestRunFailsFastAndIncludesApplicationPhaseAndPath() {
	first := s.executable("first", "printf before")
	second := filepath.Join(s.root, "missing")
	later := s.executable("later", "printf after")
	var output bytes.Buffer
	configured, err := Run(context.Background(), manifest.Resolved{CatalogDir: s.root, Applications: []manifest.Application{
		{ID: "first", PreHook: first}, {ID: "broken", PreHook: second}, {ID: "later", PreHook: later},
	}}, Pre, s.root, &output)
	s.True(configured)
	s.Require().Error(err)
	s.Contains(err.Error(), "broken")
	s.Contains(err.Error(), "pre_hook")
	s.Contains(err.Error(), second)
	s.Contains(output.String(), "before")
	s.NotContains(output.String(), "after")
}

func (s *HooksSuite) TestRunRejectsFileWithoutExecutableBit() {
	path := filepath.Join(s.root, "plain")
	s.Require().NoError(os.WriteFile(path, []byte("plain"), 0644))
	var output bytes.Buffer
	configured, err := Run(context.Background(), manifest.Resolved{CatalogDir: s.root, Applications: []manifest.Application{{ID: "plain", PreHook: path}}}, Pre, s.root, &output)
	s.True(configured)
	s.Require().Error(err)
	s.Contains(err.Error(), "regular executable file")
	s.NotContains(output.String(), "plain")
	info, statErr := os.Stat(path)
	s.Require().NoError(statErr)
	s.Equal(os.FileMode(0644), info.Mode().Perm())
}
