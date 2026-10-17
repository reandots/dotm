package installed

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"
)

type DoctorSuite struct{ suite.Suite }

func TestDoctorSuite(t *testing.T) { suite.Run(t, new(DoctorSuite)) }

func (s *DoctorSuite) TestMissingNixGitAndChezmoiAreIndependentAndReadOnly() {
	root := s.T().TempDir()
	s.T().Setenv("PATH", root)
	s.T().Setenv("HOME", root)
	s.T().Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	findings, executable := Diagnose(context.Background())
	s.Empty(executable)
	byCheck := make(map[string]DoctorFinding)
	for _, finding := range findings {
		byCheck[finding.Check] = finding
	}
	s.Equal(SeverityInternal, byCheck["nix"].Severity)
	s.Equal(SeverityInternal, byCheck["chezmoi"].Severity)
	s.Contains(byCheck["profile"].Message, "absent")
	s.Equal(SeverityInternal, byCheck["git"].Severity)
	s.NoFileExists(filepath.Join(root, "state", "nix"))
}

func (s *DoctorSuite) TestRelativeStateHomeIsInputFindingWithoutCreatingProfile() {
	root := s.T().TempDir()
	s.T().Setenv("PATH", root)
	s.T().Setenv("HOME", root)
	s.T().Setenv("XDG_STATE_HOME", "relative-state")
	findings, _ := Diagnose(context.Background())
	var profile DoctorFinding
	for _, finding := range findings {
		if finding.Check == "profile" {
			profile = finding
		}
	}
	s.Equal(SeverityConfiguration, profile.Severity)
	s.Contains(profile.Message, "absolute")
	s.NoFileExists(filepath.Join(root, "relative-state"))
}

func (s *DoctorSuite) TestCorruptManifestIsReportedWithoutNix() {
	root := s.T().TempDir()
	state := filepath.Join(root, "state")
	profile := filepath.Join(state, "nix", "profile")
	s.Require().NoError(os.MkdirAll(profile, 0o700))
	s.Require().NoError(os.WriteFile(filepath.Join(profile, "manifest.json"), []byte("broken"), 0o600))
	s.T().Setenv("PATH", root)
	s.T().Setenv("HOME", root)
	s.T().Setenv("XDG_STATE_HOME", state)
	findings, _ := Diagnose(context.Background())
	for _, finding := range findings {
		if finding.Check == "profile" {
			s.Equal(SeverityInternal, finding.Severity)
			s.Contains(finding.Message, "manifest")
			return
		}
	}
	s.Fail("missing profile corruption finding")
}

func (s *DoctorSuite) TestDanglingProfileIsNotFirstUse() {
	root := s.T().TempDir()
	state := filepath.Join(root, "state")
	profile := filepath.Join(state, "nix", "profile")
	s.Require().NoError(os.MkdirAll(filepath.Dir(profile), 0o700))
	s.Require().NoError(os.Symlink(filepath.Join(root, "missing-profile"), profile))
	s.T().Setenv("PATH", root)
	s.T().Setenv("HOME", root)
	s.T().Setenv("XDG_STATE_HOME", state)
	findings, _ := Diagnose(context.Background())
	for _, finding := range findings {
		if finding.Check == "profile" {
			s.Equal(SeverityInternal, finding.Severity)
			s.NotContains(finding.Message, "absent")
			return
		}
	}
	s.Fail("missing dangling-profile finding")
}

func (s *DoctorSuite) TestNixConfigurationAndStoreFailuresAreIndependent() {
	root := s.T().TempDir()
	bin := filepath.Join(root, "bin")
	s.Require().NoError(os.Mkdir(bin, 0o700))
	nix := `#!/bin/sh
case "$1 $2 $3" in
  "config show --json") printf '%s\n' '{"experimental-features":{"value":["flakes"]},
"use-xdg-base-directories":{"value":false}}' ;;
  "store info --json") printf '%s\n' '{"url":"local","trusted":false}' ;;
  *) exit 2 ;;
esac
`
	//nolint:gosec // G306: private executable fixture requires its owner execute bit.
	s.Require().NoError(os.WriteFile(filepath.Join(bin, "nix"), []byte(nix), 0o700))
	s.T().Setenv("PATH", bin)
	s.T().Setenv("HOME", root)
	s.T().Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	findings, _ := Diagnose(context.Background())
	featureFailures, xdgFeatureFailures, daemonFailures := 0, 0, 0
	for _, finding := range findings {
		if finding.Check == "nix-config" && finding.Severity == 3 {
			if strings.Contains(finding.Message, "experimental-features") {
				featureFailures++
			}
			if strings.Contains(finding.Message, "use-xdg-base-directories") {
				xdgFeatureFailures++
			}
		}
		if finding.Check == "nix-daemon" && finding.Severity == 3 {
			daemonFailures++
		}
	}
	s.Equal(1, featureFailures)
	s.Equal(1, xdgFeatureFailures)
	s.Equal(1, daemonFailures)
}

func (s *DoctorSuite) TestProfileSessionFindingsReportMissingBinAndShare() {
	root := s.T().TempDir()
	bin := filepath.Join(root, "bin")
	s.Require().NoError(os.Mkdir(bin, 0o700))
	git, err := exec.LookPath("git")
	s.Require().NoError(err)
	s.Require().NoError(os.Symlink(git, filepath.Join(bin, "git")))
	nix := `#!/bin/sh
case "$1 $2 $3" in
  "config show --json") printf '%s\n' '{"experimental-features":{"value":["flakes","nix-command"]},
"use-xdg-base-directories":{"value":true}}' ;;
  "store info --json") printf '%s\n' '{"url":"daemon","trusted":false}' ;;
	  "profile list --profile"*) printf '%s\n' '{"version":3,"elements":{}}' ;;
  *) exit 2 ;;
esac
`
	//nolint:gosec // G306: private executable fixture requires its owner execute bit.
	s.Require().NoError(os.WriteFile(filepath.Join(bin, "nix"), []byte(nix), 0o700))
	state := filepath.Join(root, "state")
	profile := filepath.Join(state, "nix", "profile")
	s.Require().NoError(os.MkdirAll(profile, 0o700))
	s.Require().NoError(os.WriteFile(filepath.Join(profile, "manifest.json"),
		[]byte(`{"version":3,"elements":{}}`), 0o600))
	s.T().Setenv("PATH", bin)
	s.T().Setenv("HOME", root)
	s.T().Setenv("XDG_STATE_HOME", state)
	s.T().Setenv("XDG_DATA_DIRS", "/usr/share")
	findings, executable := Diagnose(context.Background())
	s.Empty(executable)
	byCheck := make(map[string]DoctorFinding)
	for _, finding := range findings {
		byCheck[finding.Check] = finding
	}
	s.Equal(SeverityInternal, byCheck["profile-path"].Severity)
	s.Equal(SeverityInternal, byCheck["profile-data"].Severity)
}
