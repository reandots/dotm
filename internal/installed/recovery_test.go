package installed

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"dotm/internal/manifest"

	"github.com/stretchr/testify/suite"
)

type UpdateRecoverySuite struct{ suite.Suite }

func TestUpdateRecoverySuite(t *testing.T) { suite.Run(t, new(UpdateRecoverySuite)) }

const recoveryOriginalLock = `{"version":7,"root":"root","nodes":{"root":{"inputs":{"nixpkgs":"nixpkgs"}},` +
	`"nixpkgs":{"locked":{"type":"github","owner":"NixOS","repo":"nixpkgs",` +
	`"rev":"0000000000000000000000000000000000000000","narHash":"sha256-original"}}}}`

const recoveryAdvancedLock = `{"version":7,"root":"root","nodes":{"root":{"inputs":{"nixpkgs":"nixpkgs"}},` +
	`"nixpkgs":{"locked":{"type":"github","owner":"NixOS","repo":"nixpkgs",` +
	`"rev":"1111111111111111111111111111111111111111","narHash":"sha256-advanced"}}}}`

func recoveryOriginalFlake() string {
	return renderSource("x86_64-linux", []manifest.Package{{Attr: "hello"}})
}

func (s *UpdateRecoverySuite) fixture() (string, string, string) {
	s.T().Helper()
	root := s.T().TempDir()
	state := filepath.Join(root, "state")
	s.T().Setenv("XDG_STATE_HOME", state)
	bin := filepath.Join(root, "bin")
	s.Require().NoError(os.MkdirAll(bin, 0o700))
	s.T().Setenv("PATH", bin)
	profile := filepath.Join(state, "nix", "profile")
	s.Require().NoError(os.MkdirAll(filepath.Dir(profile), 0o700))
	for _, n := range []int{1, 2} {
		target := filepath.Join(root, "generation-"+strconv.Itoa(n))
		s.Require().NoError(os.MkdirAll(target, 0o700))
		manifestPath := filepath.Join(target, "manifest.json")
		s.Require().NoError(os.WriteFile(manifestPath, []byte(`{"version":3,"elements":{}}`), 0o600))
		s.Require().NoError(os.Symlink(target, profile+"-"+strconv.Itoa(n)+"-link"))
	}
	s.Require().NoError(os.Symlink("profile-1-link", profile))
	source := filepath.Join(state, "dotm", "source")
	s.T().Setenv("FAKE_PROFILE", profile)
	s.T().Setenv("FAKE_LOCK", filepath.Join(source, "flake.lock"))
	s.T().Setenv("FAKE_SOURCE", source)
	s.T().Setenv("FAIL_ADVANCE", "")
	s.T().Setenv("FAIL_UPGRADE", "")
	s.T().Setenv("FAIL_ADD", "")
	s.T().Setenv("FAIL_ROLLBACK", "")
	s.T().Setenv("ADVANCED_LOCK", recoveryAdvancedLock)
	nix := `#!/bin/sh
if [ "$1" = profile ] && [ "$2" = rollback ]; then
  if [ -e "$FAIL_ROLLBACK" ]; then exit 17; fi
  profile=
  target=
  while [ "$#" -gt 0 ]; do
    case "$1" in --profile) shift; profile=$1;; --to) shift; target=$1;; esac
    shift
  done
  /bin/ln -sfn "profile-$target-link" "$profile"
  exit 0
fi
if [ "$1" = profile ] && [ "$2" = list ]; then
  /bin/cat "$FAKE_PROFILE/manifest.json"
  exit 0
fi
if [ "$1" = flake ] && [ "$2" = update ]; then
  echo advance-started >&2
  printf '%s' "$ADVANCED_LOCK" > "$FAKE_LOCK"
  if [ -n "$FAIL_ADVANCE" ]; then exit 18; fi
  exit 0
fi
if [ "$1" = profile ] && [ "$2" = upgrade ]; then
  if [ -n "$FAIL_UPGRADE" ]; then exit 19; fi
  /bin/ln -sfn profile-2-link "$FAKE_PROFILE"
  echo upgrade-completed >&2
  exit 0
fi
if [ "$1" = profile ] && [ "$2" = add ]; then
  if [ -n "$FAIL_ADD" ]; then exit 20; fi
  /bin/ln -sfn profile-2-link "$FAKE_PROFILE"
  exit 0
fi
exit 0
`
	// #nosec G306 -- Executable fixture is private and only used by this test.
	s.Require().NoError(os.WriteFile(filepath.Join(bin, "nix"), []byte(nix), 0o700))
	return state, profile, source
}

func (s *UpdateRecoverySuite) begin(profile, source string) *updateRecovery {
	s.T().Helper()
	s.Require().NoError(os.MkdirAll(source, 0o700))
	s.Require().NoError(os.WriteFile(filepath.Join(source, "flake.nix"), []byte(recoveryOriginalFlake()), 0o600))
	s.Require().NoError(os.WriteFile(filepath.Join(source, "flake.lock"), []byte(recoveryOriginalLock), 0o600))
	r, err := beginUpdateRecovery(context.Background(), profile, io.Discard)
	s.Require().NoError(err)
	return r
}

func (s *UpdateRecoverySuite) mutate(source, profile string) {
	s.T().Helper()
	s.Require().NoError(os.WriteFile(filepath.Join(source, "flake.nix"), []byte("advanced flake\n"), 0o600))
	s.Require().NoError(os.WriteFile(filepath.Join(source, "flake.lock"), []byte(recoveryAdvancedLock), 0o600))
	s.Require().NoError(os.Remove(profile))
	s.Require().NoError(os.Symlink("profile-2-link", profile))
}

func (s *UpdateRecoverySuite) assertOriginal(source, profile string) {
	s.T().Helper()
	for name, want := range map[string]string{
		"flake.nix": recoveryOriginalFlake(), "flake.lock": recoveryOriginalLock,
	} {
		got, err := os.ReadFile(filepath.Join(source, name))
		s.Require().NoError(err)
		s.Equal(want, string(got))
	}
	link, err := os.Readlink(profile)
	s.Require().NoError(err)
	s.Equal("profile-1-link", link)
}

func (s *UpdateRecoverySuite) TestRollbackRestoresSourceAndNativeGenerationWithoutUndoingUserData() {
	_, profile, source := s.fixture()
	r := s.begin(profile, source)
	s.mutate(source, profile)
	userData := filepath.Join(s.T().TempDir(), "hook-data")
	s.Require().NoError(os.WriteFile(userData, []byte("hook side effect\n"), 0o600))
	s.Require().NoError(r.rollback(context.Background(), io.Discard))
	s.assertOriginal(source, profile)
	s.Equal("hook side effect\n", mustRead(s.T(), userData))
}

func (s *UpdateRecoverySuite) TestCommitLeavesChangesAndClearsJournal() {
	state, profile, source := s.fixture()
	r := s.begin(profile, source)
	s.mutate(source, profile)
	s.Require().NoError(r.commit())
	s.Equal("advanced flake\n", mustRead(s.T(), filepath.Join(source, "flake.nix")))
	_, err := os.Stat(filepath.Join(state, "dotm", "update-recovery.json"))
	s.ErrorIs(err, os.ErrNotExist)
}

func (s *UpdateRecoverySuite) TestPendingJournalRestoresBeforeOperationAndRetryAfterFailure() {
	state, profile, source := s.fixture()
	s.begin(profile, source)
	s.mutate(source, profile)
	failure := filepath.Join(state, "fail-rollback")
	s.T().Setenv("FAIL_ROLLBACK", failure)
	s.Require().NoError(os.WriteFile(failure, nil, 0o600))
	ctx, release, err := beginOperation(context.Background(), io.Discard)
	s.Error(err)
	s.Nil(release)
	_ = ctx
	s.Equal("advanced flake\n", mustRead(s.T(), filepath.Join(source, "flake.nix")),
		"failed recovery must retain snapshot and block the operation")
	s.Require().NoError(os.Remove(failure))
	ctx, release, err = beginOperation(context.Background(), io.Discard)
	s.Require().NoError(err)
	release()
	s.NotNil(ctx)
	s.assertOriginal(source, profile)
}

func (s *UpdateRecoverySuite) TestRollbackRestoresAbsentSourceFileAsAbsent() {
	_, profile, source := s.fixture()
	s.Require().NoError(os.MkdirAll(source, 0o700))
	s.Require().NoError(os.WriteFile(filepath.Join(source, "flake.nix"), []byte(recoveryOriginalFlake()), 0o600))
	r, err := beginUpdateRecovery(context.Background(), profile, io.Discard)
	s.Require().NoError(err)
	s.Require().NoError(os.WriteFile(filepath.Join(source, "flake.lock"), []byte("new lock\n"), 0o600))
	s.Require().NoError(r.rollback(context.Background(), io.Discard))
	_, err = os.Lstat(filepath.Join(source, "flake.lock"))
	s.ErrorIs(err, os.ErrNotExist)
	s.Equal(recoveryOriginalFlake(), mustRead(s.T(), filepath.Join(source, "flake.nix")))
}

func (s *UpdateRecoverySuite) TestCorruptJournalFailsClosed() {
	state, profile, _ := s.fixture()
	journal := filepath.Join(state, "dotm", "update-recovery.json")
	s.Require().NoError(os.MkdirAll(filepath.Dir(journal), 0o700))
	s.Require().NoError(os.WriteFile(journal, []byte(`{"version":99}`), 0o600))
	_, release, err := beginOperation(context.Background(), io.Discard)
	s.Error(err)
	s.Nil(release)
	link, readErr := os.Readlink(profile)
	s.Require().NoError(readErr)
	s.Equal("profile-1-link", link)
	s.FileExists(journal)
}

func (s *UpdateRecoverySuite) updateFixture(extraMissing bool) (string, string, string, manifest.Resolved) {
	state, profile, source := s.fixture()
	s.Require().NoError(os.MkdirAll(source, 0o700))
	s.Require().NoError(os.WriteFile(filepath.Join(source, "flake.nix"), []byte(recoveryOriginalFlake()), 0o600))
	s.Require().NoError(os.WriteFile(filepath.Join(source, "flake.lock"), []byte(recoveryOriginalLock), 0o600))
	for _, n := range []int{1, 2} {
		entry := map[string]any{
			"hello": map[string]any{
				"active": true, "attrPath": "packages.x86_64-linux.dotm_68656c6c6f",
				"originalUrl": "path:" + source, "url": "path:" + source,
				"outputs":    nil,
				"storePaths": []string{"/nix/store/hello-" + strconv.Itoa(n)},
			},
		}
		data, err := json.Marshal(map[string]any{"version": 3, "elements": entry})
		s.Require().NoError(err)
		target := filepath.Join(filepath.Dir(state), "generation-"+strconv.Itoa(n), "manifest.json")
		s.Require().NoError(os.WriteFile(target, data, 0o600))
	}
	packages := []manifest.Package{{Attr: "hello"}}
	if extraMissing {
		packages = append(packages, manifest.Package{Attr: "jq"})
	}
	desired := manifest.Resolved{
		Applications: []manifest.Application{{ID: "tools", Packages: packages}}, Packages: packages,
	}
	return state, profile, source, desired
}

func (s *UpdateRecoverySuite) assertJournalCleared(state string) {
	s.T().Helper()
	_, err := os.Lstat(filepath.Join(state, "dotm", "update-recovery.json"))
	s.ErrorIs(err, os.ErrNotExist)
}

func (s *UpdateRecoverySuite) TestSourceAdvanceFailureRestoresInitialState() {
	state, profile, source, desired := s.updateFixture(false)
	s.T().Setenv("FAIL_ADVANCE", "1")
	_, err := Update(context.Background(), desired, io.Discard, ConfigurationOptions{})
	s.Error(err)
	s.assertOriginal(source, profile)
	s.assertJournalCleared(state)
}

func (s *UpdateRecoverySuite) TestUpgradeThenAddFailureRestoresInitialState() {
	state, profile, source, desired := s.updateFixture(true)
	s.T().Setenv("FAIL_ADD", "1")
	_, err := Update(context.Background(), desired, io.Discard, ConfigurationOptions{})
	s.Error(err)
	s.assertOriginal(source, profile)
	s.assertJournalCleared(state)
}

func (s *UpdateRecoverySuite) TestSuccessfulUpdateCommitsRecoverySnapshot() {
	state, profile, source, desired := s.updateFixture(false)
	_, err := Update(context.Background(), desired, io.Discard, ConfigurationOptions{})
	s.Require().NoError(err)
	link, err := os.Readlink(profile)
	s.Require().NoError(err)
	s.Equal("profile-2-link", link)
	s.Equal(recoveryAdvancedLock, mustRead(s.T(), filepath.Join(source, "flake.lock")))
	s.assertJournalCleared(state)
}

func (s *UpdateRecoverySuite) TestConfigurationFailureKeepsSuccessfulUpdateCommitted() {
	state, profile, source, desired := s.updateFixture(false)
	desired.CatalogDir = filepath.Dir(state)
	desired.Applications[0].PostCfgHook = "failed-post"
	hook := filepath.Join(desired.CatalogDir, "failed-post")
	// #nosec G306 -- Executable fixture is private and only verifies post-update failure.
	s.Require().NoError(os.WriteFile(hook, []byte("#!/bin/sh\nexit 9\n"), 0o700))
	_, err := Update(context.Background(), desired, io.Discard, ConfigurationOptions{})
	s.Require().Error(err)
	link, readErr := os.Readlink(profile)
	s.Require().NoError(readErr)
	s.Equal("profile-2-link", link)
	s.Equal(recoveryAdvancedLock, mustRead(s.T(), filepath.Join(source, "flake.lock")))
	s.assertJournalCleared(state)
}

func (s *UpdateRecoverySuite) TestConfigureOwnsMutationAndRecoversPendingUpdate() {
	state, profile, source := s.fixture()
	s.begin(profile, source)
	s.mutate(source, profile)
	_, release, err := BeginMutation(context.Background())
	s.Require().NoError(err)
	_, err = Configure(context.Background(), manifest.Resolved{}, io.Discard, ConfigurationOptions{})
	s.ErrorIs(err, errMutationBusy)
	release()
	_, err = Configure(context.Background(), manifest.Resolved{}, io.Discard, ConfigurationOptions{})
	s.Require().NoError(err)
	s.assertOriginal(source, profile)
	s.assertJournalCleared(state)
}

type cancellingWriter struct{ cancel context.CancelFunc }

func (w cancellingWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("upgrade-completed\n")) {
		w.cancel()
	}
	return io.Discard.Write(p)
}

func (s *UpdateRecoverySuite) TestCancelledUpdateUsesDetachedContextForRollback() {
	_, profile, source, desired := s.updateFixture(false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := Update(ctx, desired, cancellingWriter{cancel: cancel}, ConfigurationOptions{})
	s.Error(err)
	s.assertOriginal(source, profile)
}

func (s *UpdateRecoverySuite) TestInstallRecoversPendingUpdateBeforePlanning() {
	_, profile, source := s.fixture()
	s.begin(profile, source)
	s.mutate(source, profile)
	// The still-published journal models process interruption after partial mutation.
	_, err := Install(context.Background(), manifest.Resolved{}, io.Discard, ConfigurationOptions{})
	s.Require().NoError(err)
	s.assertOriginal(source, profile)
}

func (s *UpdateRecoverySuite) TestCleanRecoversPendingUpdateBeforePlanning() {
	state, profile, source := s.fixture()
	s.begin(profile, source)
	s.mutate(source, profile)
	var out, stderr bytes.Buffer
	err := Clean(context.Background(), CleanOptions{Keep: 10}, nil, &out, &stderr)
	s.Require().NoError(err)
	s.Contains(out.String(), "Current generation: 1")
	s.assertOriginal(source, profile)
	s.assertJournalCleared(state)
}

func (s *UpdateRecoverySuite) TestReplacedRecoveryGenerationBlocksMutation() {
	state, profile, source := s.fixture()
	s.begin(profile, source)
	s.mutate(source, profile)
	s.Require().NoError(os.Remove(profile + "-1-link"))
	target, err := filepath.EvalSymlinks(profile + "-2-link")
	s.Require().NoError(err)
	s.Require().NoError(os.Symlink(target, profile+"-1-link"))
	_, err = Install(context.Background(), manifest.Resolved{}, io.Discard, ConfigurationOptions{})
	s.Require().Error(err)
	s.Equal("advanced flake\n", mustRead(s.T(), filepath.Join(source, "flake.nix")))
	link, err := os.Readlink(profile)
	s.Require().NoError(err)
	s.Equal("profile-2-link", link)
	s.FileExists(filepath.Join(state, "dotm", "update-recovery.json"))
}

func (s *UpdateRecoverySuite) TestDryRunDoesNotRecoverInterruptedUpdate() {
	state, profile, source := s.fixture()
	s.begin(profile, source)
	s.mutate(source, profile)
	var out bytes.Buffer
	err := Clean(context.Background(), CleanOptions{Keep: 10, DryRun: true}, nil, &out, io.Discard)
	s.Require().NoError(err)
	s.Contains(out.String(), "Current generation: 2")
	s.Equal(recoveryAdvancedLock, mustRead(s.T(), filepath.Join(source, "flake.lock")))
	s.FileExists(filepath.Join(state, "dotm", "update-recovery.json"))
}

func (s *UpdateRecoverySuite) TestRepeatedRestorationAfterPartialSourceRestore() {
	_, profile, source := s.fixture()
	r := s.begin(profile, source)
	s.mutate(source, profile)
	// Model a crash after restoring just one file, with the journal still pending.
	s.Require().NoError(os.WriteFile(filepath.Join(source, "flake.nix"), []byte(recoveryOriginalFlake()), 0o600))
	s.Require().NoError(r.rollback(context.Background(), io.Discard))
	s.assertOriginal(source, profile)
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
