package installed

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dotm/internal/manifest"

	"github.com/stretchr/testify/suite"
)

type BootstrapSuite struct{ suite.Suite }

func TestBootstrapSuite(t *testing.T) { suite.Run(t, new(BootstrapSuite)) }

func (s *BootstrapSuite) fixture() (string, string, string) {
	s.T().Helper()
	backend, err := exec.LookPath("chezmoi")
	if err != nil {
		s.T().Skip("real chezmoi required for operational compatibility probe")
	}
	git, err := exec.LookPath("git")
	s.Require().NoError(err)
	python, err := exec.LookPath("python3")
	s.Require().NoError(err)
	root := s.T().TempDir()
	state, bin, store := filepath.Join(root, "state"), filepath.Join(root, "bin"), filepath.Join(root, "store")
	for _, dir := range []string{bin, filepath.Join(store, "bin"), filepath.Join(state, "nix", "profile")} {
		s.Require().NoError(os.MkdirAll(dir, 0o700))
	}
	s.Require().NoError(os.Symlink(backend, filepath.Join(store, "bin", "chezmoi")))
	s.Require().NoError(os.Symlink(git, filepath.Join(bin, "git")))
	profile := filepath.Join(state, "nix", "profile")
	s.Require().NoError(os.WriteFile(filepath.Join(profile, "manifest.json"),
		[]byte(`{"version":3,"elements":{}}`), 0o600))
	s.T().Setenv("HOME", filepath.Join(root, "home"))
	s.T().Setenv("XDG_STATE_HOME", state)
	s.T().Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	s.T().Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	s.T().Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	s.T().Setenv("PATH", bin)
	s.T().Setenv("BOOTSTRAP_STORE", store)
	s.T().Setenv("BOOTSTRAP_FAIL", "")
	script := "#!" + python + `
import json, os, sys
from pathlib import Path
args = sys.argv[1:]
state = Path(os.environ['XDG_STATE_HOME'])
manifest = state / 'nix/profile/manifest.json'
if args[:2] == ['profile', 'list']:
    print(manifest.read_text())
    sys.exit(0)
with (state / 'mutations').open('a') as log:
    log.write(' '.join(args[:2]) + '\n')
if args[:2] == ['flake', 'lock']:
    source = Path(args[2].removeprefix('path:'))
    lock = {'version': 7, 'root': 'root', 'nodes': {
        'root': {'inputs': {'nixpkgs': 'nixpkgs'}},
        'nixpkgs': {'locked': {'type': 'github', 'owner': 'NixOS', 'repo': 'nixpkgs',
            'rev': '0' * 40, 'narHash': 'sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA='}}}}
    (source / 'flake.lock').write_text(json.dumps(lock))
elif args[:2] == ['profile', 'add']:
    if os.environ['BOOTSTRAP_FAIL']: sys.exit(23)
    data = json.loads(manifest.read_text())
    for ref in args:
        if not ref.startswith('path:'): continue
        source, attr = ref.split('#', 1)
        data['elements']['backend'] = {'active': True, 'attrPath': attr,
            'originalUrl': source, 'url': source, 'outputs': None,
            'storePaths': [os.environ['BOOTSTRAP_STORE']]}
    manifest.write_text(json.dumps(data))
elif args[:2] == ['profile', 'remove']:
    data = json.loads(manifest.read_text())
    for name in args[args.index('--') + 1:]: del data['elements'][name]
    manifest.write_text(json.dumps(data))
else: raise SystemExit('unexpected mutation: ' + str(args))
`
	// #nosec G306 -- Private executable fake Nix for isolated bootstrap scenarios.
	s.Require().NoError(os.WriteFile(filepath.Join(bin, "nix"), []byte(script), 0o700))
	return state, bin, backend
}

func (s *BootstrapSuite) TestAcquisitionRepeatSharingRemovalAndPendingRoleReconciliation() {
	state, _, backend := s.fixture()
	ctx := context.Background()
	first, err := BootstrapChezmoi(ctx, io.Discard)
	s.Require().NoError(err)
	s.Equal(backend, first.Executable)
	s.True(first.Retained)
	s.Require().Len(first.Report.Infrastructure, 1)
	s.Empty(first.Report.Extras)
	lock := mustRead(s.T(), filepath.Join(first.Source, "flake.lock"))
	mutations := mustRead(s.T(), filepath.Join(state, "mutations"))
	role, err := readInfrastructure(state)
	s.Require().NoError(err)
	s.False(role.Pending)
	role.Pending = true // Simulate death after add, before final role recording.
	s.Require().NoError(writeInfrastructure(state, role))
	reconciled, err := BootstrapChezmoi(ctx, io.Discard)
	s.Require().NoError(err)
	s.Equal(first.Executable, reconciled.Executable)
	role, err = readInfrastructure(state)
	s.Require().NoError(err)
	s.False(role.Pending)
	repeated, err := BootstrapChezmoi(ctx, io.Discard)
	s.Require().NoError(err)
	s.Empty(repeated.Effects)
	s.Equal(mutations, mustRead(s.T(), filepath.Join(state, "mutations")))
	s.Equal(lock, mustRead(s.T(), filepath.Join(first.Source, "flake.lock")))
	pkg := manifest.Package{Attr: "chezmoi"}
	desired := manifest.Resolved{
		Packages:     []manifest.Package{pkg},
		Applications: []manifest.Application{{ID: "shared", Packages: []manifest.Package{pkg}}},
	}
	shared, err := Inspect(ctx, desired)
	s.Require().NoError(err)
	s.True(shared.Applications[0].Ready)
	s.Len(shared.Entries, 1)
	// Dropping that catalog application does not authorize backend removal.
	removed, err := RemoveExtras(ctx, &manifest.Data{}, manifest.Resolved{}, true, strings.NewReader(""), io.Discard)
	s.Require().NoError(err)
	s.Equal(first.Report.Entries, removed.Entries)
	// Simulated later caller failure cannot undo or hide the successful acquisition.
	s.NotEmpty(first.Effects)
	s.Equal(first.Executable, repeated.Executable)
}

func (s *BootstrapSuite) TestExternalReuseDoesNotClaimOwnership() {
	state, bin, backend := s.fixture()
	s.Require().NoError(os.Symlink(backend, filepath.Join(bin, "chezmoi")))
	result, err := BootstrapChezmoi(context.Background(), io.Discard)
	s.Require().NoError(err)
	s.Equal(backend, result.Executable)
	s.False(result.Retained)
	s.Empty(result.Effects)
	s.NoFileExists(filepath.Join(state, "dotm", "infrastructure.json"))
	s.NoFileExists(filepath.Join(state, "mutations"))
}

func (s *BootstrapSuite) TestUnusableCandidatesAndPrerequisitesFailBeforeMutation() {
	for _, scenario := range []string{"incompatible", "non-executable", "dangling", "missing-git", "pending-recovery"} {
		s.Run(scenario, func() {
			state, bin, _ := s.fixture()
			candidate := filepath.Join(bin, "chezmoi")
			switch scenario {
			case "incompatible":
				// #nosec G306 -- Private incompatible executable fixture.
				s.Require().NoError(os.WriteFile(candidate, []byte("#!/bin/sh\nexit 7\n"), 0o700))
			case "non-executable":
				s.Require().NoError(os.WriteFile(candidate, []byte("not executable"), 0o600))
			case "dangling":
				s.Require().NoError(os.Symlink(filepath.Join(bin, "absent"), candidate))
			case "missing-git":
				s.Require().NoError(os.Remove(filepath.Join(bin, "git")))
			case "pending-recovery":
				s.Require().NoError(os.MkdirAll(filepath.Join(state, "dotm"), 0o700))
				journal := filepath.Join(state, "dotm", updateJournalName)
				s.Require().NoError(os.WriteFile(journal, []byte("pending"), 0o600))
			}
			_, err := BootstrapChezmoi(context.Background(), io.Discard)
			s.Require().Error(err)
			s.NoFileExists(filepath.Join(state, "mutations"))
			s.NoFileExists(filepath.Join(state, "dotm", "infrastructure.json"))
		})
	}
}

func (s *BootstrapSuite) TestFailedAddKeepsRecoverableIntentAndSource() {
	state, _, _ := s.fixture()
	s.T().Setenv("BOOTSTRAP_FAIL", "1")
	failed, err := BootstrapChezmoi(context.Background(), io.Discard)
	s.Require().Error(err)
	s.Empty(failed.Executable)
	s.NotEmpty(failed.Source)
	s.NotEmpty(failed.Effects)
	role, err := readInfrastructure(state)
	s.Require().NoError(err)
	s.True(role.Pending)
	lock := mustRead(s.T(), filepath.Join(failed.Source, "flake.lock"))
	s.T().Setenv("BOOTSTRAP_FAIL", "")
	retry, err := BootstrapChezmoi(context.Background(), io.Discard)
	s.Require().NoError(err)
	s.Equal(failed.Source, retry.Source)
	s.Equal(lock, mustRead(s.T(), filepath.Join(retry.Source, "flake.lock")))
	s.Len(retry.Report.Infrastructure, 1)
}

func (s *BootstrapSuite) TestUnrelatedExtrasRemainRemovable() {
	_, _, _ = s.fixture()
	ctx := context.Background()
	result, err := BootstrapChezmoi(ctx, io.Discard)
	s.Require().NoError(err)
	path := filepath.Join(result.Report.Profile, "manifest.json")
	var data struct {
		Version  int                        `json:"version"`
		Elements map[string]json.RawMessage `json:"elements"`
	}
	s.Require().NoError(json.Unmarshal([]byte(mustRead(s.T(), path)), &data))
	data.Elements["unrelated"] = json.RawMessage(`{"active":true,"storePaths":["/nix/store/unrelated-chezmoi"]}`)
	content, err := json.Marshal(data)
	s.Require().NoError(err)
	s.Require().NoError(os.WriteFile(path, content, 0o600))
	removed, err := RemoveExtras(ctx, &manifest.Data{}, manifest.Resolved{}, true, strings.NewReader(""), io.Discard)
	s.Require().NoError(err)
	s.Empty(removed.Extras)
	s.Equal(result.Report.Entries, removed.Entries)
}

func (s *BootstrapSuite) TestCanonicalRoleSurvivesSourceLossWithoutAdoptingOtherEntries() {
	state, _, _ := s.fixture()
	ctx := context.Background()
	first, err := BootstrapChezmoi(ctx, io.Discard)
	s.Require().NoError(err)
	path := filepath.Join(first.Report.Profile, "manifest.json")
	var data struct {
		Version  int                        `json:"version"`
		Elements map[string]json.RawMessage `json:"elements"`
	}
	s.Require().NoError(json.Unmarshal([]byte(mustRead(s.T(), path)), &data))
	foreign := sourceEntry("packages.x86_64-linux.dotm_68656c6c6f", "path:"+first.Source, nil)
	encoded, err := json.Marshal(foreign)
	s.Require().NoError(err)
	data.Elements["old-app"] = encoded
	encoded, err = json.Marshal(data)
	s.Require().NoError(err)
	s.Require().NoError(os.WriteFile(path, encoded, 0o600))
	s.Require().NoError(os.Remove(filepath.Join(first.Source, "flake.lock")))
	report, err := Inspect(ctx, manifest.Resolved{})
	s.Require().NoError(err)
	s.Require().Len(report.Infrastructure, 1)
	s.Require().Len(report.Extras, 1)
	s.Equal("old-app", report.Extras[0].Name)
	s.Empty(report.Extras[0].Descriptor)
	// Neither basename nor a descriptor in an old source adopts unrelated apps.
	retained := report.Infrastructure[0]
	s.Equal("chezmoi", retained.Descriptor)
	missing, packages, _, err := installationPlan(report, manifest.Resolved{}, "")
	s.Require().NoError(err)
	s.Empty(missing)
	s.Equal([]manifest.Package{{Attr: "chezmoi"}}, packages)
	// Only explicit application scope makes infrastructure an update target.
	s.Empty(updateSelection(report, manifest.Resolved{}).Packages)
	pkg := manifest.Package{Attr: "chezmoi"}
	desired := manifest.Resolved{
		Packages:     []manifest.Package{pkg},
		Applications: []manifest.Application{{ID: "shared", Packages: []manifest.Package{pkg}}},
	}
	report = classify(Report{Entries: report.Entries}, desired)
	selected := updateSelection(report, desired)
	s.Equal(desired.Packages, selected.Packages)
	names, err := updateTargets(report, selected, first.Source)
	s.Require().NoError(err)
	s.Equal([]string{retained.Name}, names)
	// Recovering the source must not be necessary merely to reuse its backend.
	reused, err := BootstrapChezmoi(ctx, io.Discard)
	s.Require().NoError(err)
	s.Equal(first.Executable, reused.Executable)
	s.Empty(reused.Effects)
	role, err := readInfrastructure(state)
	s.Require().NoError(err)
	s.Equal(first.Source, role.Source)
}

func (s *BootstrapSuite) TestRoleStateRejectsCorruptionAndUnsafeFiles() {
	for _, scenario := range []string{"corrupt", "foreign-descriptor", "foreign-source", "public", "symlink"} {
		s.Run(scenario, func() {
			base := s.T().TempDir()
			role := infrastructureState{
				Descriptor: infrastructureDescriptor, Source: filepath.Join(base, "dotm", "source"),
			}
			s.Require().NoError(writeInfrastructure(base, role))
			path := filepath.Join(base, "dotm", "infrastructure.json")
			switch scenario {
			case "corrupt":
				s.Require().NoError(os.WriteFile(path, []byte("not-json"), 0o600))
			case "foreign-descriptor":
				role.Descriptor = "other"
				data, err := json.Marshal(role)
				s.Require().NoError(err)
				s.Require().NoError(os.WriteFile(path, data, 0o600))
			case "foreign-source":
				role.Source = filepath.Join(base, "foreign")
				data, err := json.Marshal(role)
				s.Require().NoError(err)
				s.Require().NoError(os.WriteFile(path, data, 0o600))
			case "public":
				s.Require().NoError(os.Chmod(path, 0o644))
			case "symlink":
				s.Require().NoError(os.Rename(path, path+".old"))
				s.Require().NoError(os.Symlink(path+".old", path))
			}
			_, err := readInfrastructure(base)
			s.Require().Error(err)
		})
	}
}

func (s *BootstrapSuite) TestRecordingExistingCanonicalBackendDoesNotReportPackageMutation() {
	state, _, _ := s.fixture()
	ctx := context.Background()
	first, err := BootstrapChezmoi(ctx, io.Discard)
	s.Require().NoError(err)
	s.Require().NoError(os.Remove(filepath.Join(state, "dotm", "infrastructure.json")))
	mutations := mustRead(s.T(), filepath.Join(state, "mutations"))
	reused, err := BootstrapChezmoi(ctx, io.Discard)
	s.Require().NoError(err)
	s.Equal(first.Executable, reused.Executable)
	s.Equal(mutations, mustRead(s.T(), filepath.Join(state, "mutations")))
	s.Equal([]BootstrapEffect{{
		Resource: "state", Path: filepath.Join(state, "dotm", "infrastructure.json"), Action: "created",
	}}, reused.Effects)
}
