package cli_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dotm/internal/cli"
)

func (s *FilesCLISuite) doctorHost() {
	bin := filepath.Join(s.base, "bin")
	s.Require().NoError(os.MkdirAll(bin, 0o700))
	//nolint:gosec // G306: private executable fixture requires its owner execute bit.
	s.Require().NoError(os.WriteFile(filepath.Join(bin, "nix"), []byte(`#!/bin/sh
case "$*" in
  'config show --json') printf '%s\n' '{"experimental-features":{"value":["nix-command","flakes"]},
"use-xdg-base-directories":{"value":true}}' ;;
  'store info --json') printf '%s\n' '{"url":"daemon","trusted":false}' ;;
  *) printf 'unexpected mutation or command: %s\n' "$*" >&2; exit 91 ;;
esac
`), 0o700))
	profile := filepath.Join(s.base, "state", "nix", "profile")
	s.T().Setenv("PATH", bin+string(os.PathListSeparator)+filepath.Join(profile, "bin")+
		string(os.PathListSeparator)+os.Getenv("PATH"))
	s.T().Setenv("XDG_DATA_DIRS", filepath.Join(profile, "share")+":/usr/local/share:/usr/share")
}

func (s *FilesCLISuite) doctor() (string, string, error) {
	var out, stderr bytes.Buffer
	err := cli.Run([]string{"--data-dir", s.dataDir, "doctor"}, strings.NewReader(""), &out, &stderr)
	// Match main's single error-emission boundary without merging the two streams.
	if err != nil {
		fmt.Fprintln(&stderr, err)
	}
	return out.String(), stderr.String(), err
}

func (s *FilesCLISuite) TestDoctorChecksPublishedPolicyWithoutWriting() {
	s.doctorHost()
	output, err := s.run("init")
	s.Require().NoError(err, output)
	before, err := os.ReadFile(s.policyPath)
	s.Require().NoError(err)
	out, diagnostics, err := s.doctor()
	s.Require().NoError(err, diagnostics)
	s.Contains(out, "effective policy")
	s.NotContains(out, "blocked:")
	s.Empty(diagnostics)
	after, err := os.ReadFile(s.policyPath)
	s.Require().NoError(err)
	s.Equal(before, after)
	_, err = os.Lstat(filepath.Join(s.base, "state", "nix", "profile"))
	s.ErrorIs(err, os.ErrNotExist)
	_, err = os.Stat(filepath.Join(s.source, ".git", "FETCH_HEAD"))
	s.ErrorIs(err, os.ErrNotExist)

	s.write(s.policyPath, `{}`)
	out, diagnostics, err = s.doctor()
	s.Require().Error(err)
	s.Equal(1, cli.ExitCode(err), diagnostics)
	s.NotContains(out, "stale or mismatched")
	s.Equal(1, strings.Count(diagnostics, "stale or mismatched"), diagnostics)
	s.NotContains(diagnostics, "[0]")
	unchanged, err := os.ReadFile(s.policyPath)
	s.Require().NoError(err)
	s.Equal("{}", string(unchanged))
}

func (s *FilesCLISuite) TestDoctorPassiveFindingsSurviveBrokenManifestsAndMissingBackends() {
	s.write(filepath.Join(s.source, "run_forbidden.sh"), "echo must-not-run\n")
	s.write(filepath.Join(s.dataDir, "catalog.toml"), "invalid = [")
	s.T().Setenv("PATH", filepath.Join(s.base, "empty-path"))
	out, diagnostics, err := s.doctor()
	s.Require().Error(err)
	s.Equal(3, cli.ExitCode(err))
	s.Contains(diagnostics, "run_forbidden.sh")
	s.Contains(diagnostics, "catalog.toml")
	s.Contains(diagnostics, "git")
	s.Contains(diagnostics, "nix")
	s.Contains(diagnostics, "chezmoi")
	s.Contains(out, "blocked: effective policy")
	s.NotContains(out, "run_forbidden.sh")
	s.Equal(1, strings.Count(diagnostics, "run_forbidden.sh"))
}

func (s *FilesCLISuite) TestDoctorRejectsRepairAndMutationOptions() {
	for _, args := range [][]string{{"doctor", "--fix"}, {"doctor", "--yes"}, {"doctor", "-i"}} {
		output, err := s.run(args...)
		s.Require().Error(err, output)
		s.Equal(3, cli.ExitCode(err))
	}
}

func (s *FilesCLISuite) TestDoctorReportsSelectionAndManifestFailuresTogether() {
	s.doctorHost()
	output, err := s.run("init")
	s.Require().NoError(err, output)
	s.write(filepath.Join(s.configHome, "dotm", "config.toml"), "configuration = [")
	s.write(filepath.Join(s.dataDir, "catalog.toml"), "invalid = [")
	out, diagnostics, err := s.doctor()
	s.Require().Error(err)
	s.Equal(1, cli.ExitCode(err), diagnostics)
	s.Contains(diagnostics, filepath.Join(s.configHome, "dotm", "config.toml"))
	s.Contains(diagnostics, filepath.Join(s.dataDir, "catalog.toml"))
	s.Contains(out, "blocked: effective policy")
}
