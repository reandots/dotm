package installed

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"dotm/internal/manifest"

	"github.com/stretchr/testify/suite"
)

type ChezmoiBootstrapSuite struct{ suite.Suite }

func TestChezmoiBootstrapSuite(t *testing.T) { suite.Run(t, new(ChezmoiBootstrapSuite)) }

func (s *ChezmoiBootstrapSuite) TestAbsentBackendDoesNotCreateState() {
	root := s.T().TempDir()
	s.T().Setenv("HOME", root)
	s.T().Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	s.T().Setenv("PATH", filepath.Join(root, "bin"))
	executable, err := FindChezmoi(context.Background())
	s.Require().NoError(err)
	s.Empty(executable)
	s.NoDirExists(filepath.Join(root, "state"))
}

func (s *ChezmoiBootstrapSuite) backend() string {
	s.T().Helper()
	backend, err := exec.LookPath("chezmoi")
	if err != nil {
		s.T().Skip("chezmoi is required for bootstrap capability tests")
	}
	backend, err = filepath.EvalSymlinks(backend)
	s.Require().NoError(err)
	return backend
}

func (s *ChezmoiBootstrapSuite) TestIncompatiblePATHCandidateIsNotShadowedByProfile() {
	backend := s.backend()
	root := s.T().TempDir()
	state := filepath.Join(root, "state")
	pathBin := filepath.Join(root, "path-bin")
	profileBin := filepath.Join(state, "nix", "profile", "bin")
	for _, dir := range []string{pathBin, profileBin} {
		s.Require().NoError(os.MkdirAll(dir, 0o700))
	}
	// #nosec G306 -- Private executable fixture deliberately fails compatibility probing.
	s.Require().NoError(os.WriteFile(filepath.Join(pathBin, "chezmoi"), []byte("#!/bin/sh\nexit 1\n"), 0o700))
	s.Require().NoError(os.Symlink(backend, filepath.Join(profileBin, "chezmoi")))
	s.T().Setenv("HOME", root)
	s.T().Setenv("XDG_STATE_HOME", state)
	s.T().Setenv("PATH", pathBin)
	executable, err := FindChezmoi(context.Background())
	s.Require().Error(err)
	s.Empty(executable)
	operational, err := resolveOperationalChezmoi(context.Background())
	s.Require().NoError(err)
	s.Equal(backend, operational)
}

func (s *ChezmoiBootstrapSuite) TestFailedAddRetainsEffectsAndRetryInstallsOnlyApplicationClosure() {
	backend := s.backend()
	git, err := exec.LookPath("git")
	s.Require().NoError(err)
	fixture := &SourceStateRecoverySuite{}
	fixture.SetT(s.T())
	state, profile, source := fixture.fixture()
	bin := filepath.Join(filepath.Dir(state), "bin")
	s.Require().NoError(os.Symlink(git, filepath.Join(bin, "git")))
	s.T().Setenv("HOME", filepath.Dir(state))
	s.T().Setenv("BOOTSTRAP_BACKEND", backend)
	nixPath := filepath.Join(bin, "nix")
	nix, err := os.ReadFile(nixPath)
	s.Require().NoError(err)
	nix = append(nix, []byte(`
if args[:2] == ['profile', 'add']:
    binary = Path(os.environ['FAKE_PROFILE']) / 'bin' / 'chezmoi'
    binary.parent.mkdir(parents=True, exist_ok=True)
    binary.symlink_to(os.environ['BOOTSTRAP_BACKEND'])
`)...)
	// #nosec G306 G703 -- Existing private fake Nix path is obtained only from this test fixture.
	s.Require().NoError(os.WriteFile(nixPath, nix, 0o700))
	hook := filepath.Join(filepath.Dir(state), "pre")
	for _, id := range []string{"helper", "chezmoi"} {
		// #nosec G306 -- Private hooks log their names to prove dependency-first execution.
		s.Require().NoError(os.WriteFile(hook+"-"+id,
			[]byte("#!/bin/sh\nprintf '%s\\n' \"${0##*/}\" >> \"$HOME/pre.log\"\n"), 0o700))
	}
	data := manifest.Data{CatalogDir: filepath.Dir(state), Apps: map[string]manifest.Application{
		"helper": {ID: "helper", Packages: []manifest.Package{{Attr: "jq"}}, PreHook: hook + "-helper"},
		"chezmoi": {
			ID: "chezmoi", Dependencies: []string{"helper"}, Packages: []manifest.Package{{Attr: "chezmoi"}},
			PreHook: hook + "-chezmoi", PreCfgHook: "must-not-run",
			Configs: []string{filepath.Join(state, "destination")},
		},
	}}
	desired, err := data.Resolve([]string{"chezmoi"})
	s.Require().NoError(err)
	lock := mustRead(s.T(), filepath.Join(source, "flake.lock"))
	s.T().Setenv("FAKE_FAIL_ADD", "1")
	failed, err := BootstrapChezmoi(context.Background(), desired, io.Discard)
	s.Require().Error(err)
	s.Equal(lock, mustRead(s.T(), filepath.Join(source, "flake.lock")))
	s.Equal("pre-helper\npre-chezmoi\n", mustRead(s.T(), filepath.Join(filepath.Dir(state), "pre.log")))
	s.Empty(failed.Executable)
	// The generated source expansion is retained even when the native add failed.
	paths := make([]string, 0, len(failed.Effects))
	for _, effect := range failed.Effects {
		paths = append(paths, effect.Path)
	}
	s.Contains(paths, filepath.Join(source, "flake.nix"))
	s.T().Setenv("FAKE_FAIL_ADD", "")
	result, err := BootstrapChezmoi(context.Background(), desired, io.Discard)
	s.Require().NoError(err)
	s.Equal(backend, result.Executable)
	s.NoFileExists(filepath.Join(state, "destination"))
	s.NoFileExists(filepath.Join(state, "dotm", "infrastructure.json"))
	report, err := Inspect(context.Background(), desired)
	s.Require().NoError(err)
	s.Len(report.Extras, 1, "unrelated existing hello remains removable")
	for _, app := range report.Applications {
		s.True(app.Ready, app.ID)
	}
	before := mustRead(s.T(), filepath.Join(profile, "manifest.json"))
	repeated, err := BootstrapChezmoi(context.Background(), desired, io.Discard)
	s.Require().NoError(err)
	s.Empty(repeated.Effects)
	s.Equal(before, mustRead(s.T(), filepath.Join(profile, "manifest.json")))
}

func (s *ChezmoiBootstrapSuite) TestPendingRecoveryIsRefusedBeforeBackendReuse() {
	root := s.T().TempDir()
	s.T().Setenv("XDG_STATE_HOME", root)
	s.Require().NoError(os.MkdirAll(filepath.Join(root, "dotm"), 0o700))
	journal := filepath.Join(root, "dotm", "update-recovery.json")
	s.Require().NoError(os.WriteFile(journal, []byte("pending"), 0o600))
	_, err := BootstrapChezmoi(context.Background(), manifest.Resolved{}, io.Discard)
	s.Require().ErrorContains(err, "pending")
	s.Equal("pending", mustRead(s.T(), journal))
}
