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

type SourceStateRecoverySuite struct{ suite.Suite }

func TestSourceStateRecoverySuite(t *testing.T) { suite.Run(t, new(SourceStateRecoverySuite)) }

func (s *SourceStateRecoverySuite) fixture() (string, string, string) {
	s.T().Helper()
	root := s.T().TempDir()
	state := filepath.Join(root, "state")
	profile := filepath.Join(state, "nix", "profile")
	s.Require().NoError(os.MkdirAll(profile, 0o700))
	bin := filepath.Join(root, "bin")
	s.Require().NoError(os.MkdirAll(bin, 0o700))
	python, err := exec.LookPath("python3")
	s.Require().NoError(err)
	s.T().Setenv("XDG_STATE_HOME", state)
	s.T().Setenv("PATH", bin)
	s.T().Setenv("FAKE_PROFILE", profile)
	s.T().Setenv("FAKE_FAIL_ADD", "")
	s.T().Setenv("FAKE_MUTATIONS", filepath.Join(state, "mutations"))
	nix := "#!" + python + `
import json, os, sys
from pathlib import Path
args = sys.argv[1:]
manifest = Path(os.environ['FAKE_PROFILE']) / 'manifest.json'
if args[:2] == ['profile', 'list']:
    print(manifest.read_text())
    sys.exit(0)
with open(os.environ['FAKE_MUTATIONS'], 'a') as log:
    log.write(' '.join(args[:2]) + '\n')
if args[:2] == ['flake', 'lock']:
    source = Path(args[2].removeprefix('path:'))
    lock = {'version': 7, 'root': 'root', 'nodes': {
        'root': {'inputs': {'nixpkgs': 'nixpkgs'}},
        'nixpkgs': {'locked': {'type': 'github', 'owner': 'NixOS', 'repo': 'nixpkgs',
            'rev': '0' * 40, 'narHash': 'sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA='}}}}
    (source / 'flake.lock').write_text(json.dumps(lock))
elif args[:2] == ['profile', 'add']:
    if os.environ['FAKE_FAIL_ADD']:
        print('conflicting files in profile: bin/hello', file=sys.stderr)
        sys.exit(23)
    data = json.loads(manifest.read_text())
    for ref in args:
        if not ref.startswith('path:'): continue
        source, attr = ref.split('#', 1)
        attr, sep, outputs = attr.partition('^')
        key = 'new-' + str(len(data['elements']))
        data['elements'][key] = {'active': True, 'attrPath': attr,
            'originalUrl': source, 'url': source, 'outputs': outputs.split(',') if sep else None,
            'storePaths': ['/nix/store/' + key]}
    manifest.write_text(json.dumps(data))
elif args[:2] == ['profile', 'remove']:
    data = json.loads(manifest.read_text())
    for name in args[args.index('--') + 1:]: del data['elements'][name]
    manifest.write_text(json.dumps(data))
else:
    raise SystemExit('unexpected native mutation: ' + str(args))
`
	// #nosec G306 -- Private executable fixture for this test only.
	s.Require().NoError(os.WriteFile(filepath.Join(bin, "nix"), []byte(nix), 0o700))
	source := filepath.Join(state, "dotm", "source")
	s.Require().NoError(os.MkdirAll(source, 0o700))
	s.Require().NoError(os.WriteFile(filepath.Join(source, "flake.nix"), []byte(recoveryOriginalFlake()), 0o600))
	s.Require().NoError(os.WriteFile(filepath.Join(source, "flake.lock"), []byte(recoveryOriginalLock), 0o600))
	entries := map[string]any{
		"old-hello": sourceEntry("packages.x86_64-linux.dotm_68656c6c6f", "path:"+source, nil),
		"direct-jq": sourceEntry("legacyPackages.x86_64-linux.jq", "github:NixOS/nixpkgs/revision", nil),
	}
	data, err := json.Marshal(map[string]any{"version": 3, "elements": entries})
	s.Require().NoError(err)
	s.Require().NoError(os.WriteFile(filepath.Join(profile, "manifest.json"), data, 0o600))
	return state, profile, source
}

func (s *SourceStateRecoverySuite) desired() manifest.Resolved {
	packages := []manifest.Package{{Attr: "hello"}, {Attr: "jq"}}
	return manifest.Resolved{
		Applications: []manifest.Application{{ID: "tools", Packages: packages}}, Packages: packages,
	}
}

func (s *SourceStateRecoverySuite) TestDegradedSourcesRemainExternalAndUpdateIsNoOp() {
	for _, corruption := range []string{
		"missing-state", "missing-flake", "corrupt-flake", "missing-lock", "corrupt-lock",
		"missing-pointer", "corrupt-pointer",
	} {
		s.Run(corruption, func() {
			state, profile, source := s.fixture()
			switch corruption {
			case "missing-state":
				s.Require().NoError(os.RemoveAll(filepath.Join(state, "dotm")))
			case "missing-flake":
				s.Require().NoError(os.Remove(filepath.Join(source, "flake.nix")))
			case "corrupt-flake":
				s.Require().NoError(os.WriteFile(filepath.Join(source, "flake.nix"), []byte("invalid"), 0o600))
			case "missing-lock":
				s.Require().NoError(os.Remove(filepath.Join(source, "flake.lock")))
			case "corrupt-lock":
				s.Require().NoError(os.WriteFile(filepath.Join(source, "flake.lock"), []byte(`{"version":7}`), 0o600))
			case "missing-pointer":
				s.Require().NoError(os.MkdirAll(filepath.Join(state, "dotm", "sources"), 0o700))
			case "corrupt-pointer":
				pointer := filepath.Join(state, "dotm", sourcePointerName)
				s.Require().NoError(os.WriteFile(pointer, []byte("../bad"), 0o600))
			}
			before := mustRead(s.T(), filepath.Join(profile, "manifest.json"))
			desired := s.desired()
			desired.Applications[0].PreHook = filepath.Join(state, "absent-hook")
			report, err := Inspect(context.Background(), desired)
			s.Require().NoError(err)
			s.Equal("missing", report.Applications[0].State)
			s.Equal(report.Entries, report.Extras)
			for _, entry := range report.Entries {
				s.Empty(entry.Descriptor)
			}
			updated, err := Update(context.Background(), desired, io.Discard, ConfigurationOptions{})
			s.Require().NoError(err)
			s.Equal(report.Profile, updated.Profile)
			s.Equal(report.Applications, updated.Applications)
			s.Equal(report.Entries, updated.Entries)
			s.Equal(report.Extras, updated.Extras)
			s.Require().Len(updated.Configuration.Applications, 1)
			s.False(updated.Configuration.Applications[0].Eligible)
			s.Equal(StageSkipped, updated.Configuration.Applications[0].Pre.Status)
			s.Equal(before, mustRead(s.T(), filepath.Join(profile, "manifest.json")))
			s.NoFileExists(filepath.Join(state, "mutations"))
		})
	}
}

func (s *SourceStateRecoverySuite) TestInstallPreservesExternalEntriesAndDoesNotReadoptThem() {
	state, profile, source := s.fixture()
	s.Require().NoError(os.Remove(filepath.Join(source, "flake.lock")))
	report, err := Install(context.Background(), s.desired(), io.Discard, ConfigurationOptions{})
	s.Require().NoError(err)
	s.Equal("installed", report.Applications[0].State)
	s.Equal([]string{"direct-jq", "old-hello"}, []string{report.Extras[0].Name, report.Extras[1].Name})
	for _, entry := range report.Extras {
		s.Empty(entry.Descriptor)
	}
	pointer := mustRead(s.T(), filepath.Join(state, "dotm", sourcePointerName))
	fresh := filepath.Join(state, "dotm", "sources", strings.TrimSpace(pointer))
	s.Require().NotEqual(source, fresh)
	s.Equal(recoveryOriginalFlake(), mustRead(s.T(), filepath.Join(source, "flake.nix")))
	s.NoFileExists(filepath.Join(source, "flake.lock"))
	before := mustRead(s.T(), filepath.Join(profile, "manifest.json"))
	mutations := mustRead(s.T(), filepath.Join(state, "mutations"))
	again, err := Install(context.Background(), s.desired(), io.Discard, ConfigurationOptions{})
	s.Require().NoError(err)
	s.Equal(report, again)
	s.Equal(before, mustRead(s.T(), filepath.Join(profile, "manifest.json")))
	s.Equal(mutations, mustRead(s.T(), filepath.Join(state, "mutations")))
}

func (s *SourceStateRecoverySuite) TestExternalRemovalPreservesNewDesiredEntries() {
	_, _, source := s.fixture()
	s.Require().NoError(os.Remove(filepath.Join(source, "flake.lock")))
	desired := s.desired()
	before, err := Install(context.Background(), desired, io.Discard, ConfigurationOptions{})
	s.Require().NoError(err)
	removed, err := RemoveExtras(context.Background(), &manifest.Data{}, desired,
		true, strings.NewReader(""), io.Discard)
	s.Require().NoError(err)
	s.Equal("installed", removed.Applications[0].State)
	s.Empty(removed.Extras)
	for _, entry := range removed.Entries {
		s.NotContains([]string{"old-hello", "direct-jq"}, entry.Name)
		s.Contains(before.Entries, entry)
	}
}

func (s *SourceStateRecoverySuite) TestRepeatedLossRotatesAndConflictPreservesAllOldEntries() {
	state, profile, source := s.fixture()
	s.Require().NoError(os.Remove(filepath.Join(source, "flake.lock")))
	_, err := Install(context.Background(), s.desired(), io.Discard, ConfigurationOptions{})
	s.Require().NoError(err)
	pointer := filepath.Join(state, "dotm", sourcePointerName)
	first := mustRead(s.T(), pointer)
	firstSource := filepath.Join(state, "dotm", "sources", strings.TrimSpace(first))
	s.Require().NoError(os.WriteFile(filepath.Join(firstSource, "flake.lock"), []byte("invalid"), 0o600))
	before := mustRead(s.T(), filepath.Join(profile, "manifest.json"))
	s.T().Setenv("FAKE_FAIL_ADD", "1")
	failed, err := Install(context.Background(), s.desired(), io.Discard, ConfigurationOptions{})
	s.Require().Error(err)
	s.Equal("missing", failed.Applications[0].State)
	s.Equal(failed.Entries, failed.Extras)
	for _, entry := range failed.Entries {
		s.Empty(entry.Descriptor)
	}
	s.Equal(before, mustRead(s.T(), filepath.Join(profile, "manifest.json")))
	s.NotEqual(first, mustRead(s.T(), pointer))
	s.Equal("invalid", mustRead(s.T(), filepath.Join(firstSource, "flake.lock")))
}

func (s *SourceStateRecoverySuite) TestNativeProfileCorruptionIsNotAnEmptyEnvironment() {
	state, profile, _ := s.fixture()
	path := filepath.Join(profile, "manifest.json")
	s.Require().NoError(os.WriteFile(path, []byte("{corrupt"), 0o600))
	_, err := Install(context.Background(), s.desired(), io.Discard, ConfigurationOptions{})
	s.Require().Error(err)
	_, err = Update(context.Background(), s.desired(), io.Discard, ConfigurationOptions{})
	s.Require().Error(err)
	s.Equal("{corrupt", mustRead(s.T(), path))
	s.NoFileExists(filepath.Join(state, "mutations"))
}
