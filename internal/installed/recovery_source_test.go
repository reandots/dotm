package installed

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"dotm/internal/manifest"
)

func (s *UpdateRecoverySuite) rotatedFixture() (string, string, string) {
	s.T().Helper()
	state, profile, legacy := s.fixture()
	s.Require().NoError(os.MkdirAll(legacy, 0o700))
	s.Require().NoError(os.WriteFile(filepath.Join(legacy, "flake.nix"), []byte("legacy untouched"), 0o600))
	id := strings.Repeat("a", 32)
	source := filepath.Join(state, "dotm", "sources", id)
	s.Require().NoError(os.MkdirAll(source, 0o700))
	s.Require().NoError(os.WriteFile(filepath.Join(state, "dotm", sourcePointerName), []byte(id+"\n"), 0o600))
	return state, profile, source
}

func (s *UpdateRecoverySuite) TestRotatedSourceRecoveryUsesRecordedInstance() {
	for _, commit := range []bool{false, true} {
		name := "rollback"
		if commit {
			name = "commit"
		}
		s.Run(name, func() {
			state, profile, source := s.rotatedFixture()
			r := s.begin(profile, source)
			s.mutate(source, profile)
			if commit {
				s.Require().NoError(r.commit())
				s.Equal("advanced flake\n", mustRead(s.T(), filepath.Join(source, "flake.nix")))
				link, err := os.Readlink(profile)
				s.Require().NoError(err)
				s.Equal("profile-2-link", link)
			} else {
				s.Require().NoError(recoverJournal(context.Background(), r.path, io.Discard))
				s.assertOriginal(source, profile)
			}
			s.Equal("legacy untouched", mustRead(s.T(), filepath.Join(state, "dotm", "source", "flake.nix")))
			s.assertJournalCleared(state)
		})
	}
}

func (s *UpdateRecoverySuite) TestReplacedSourcePointerBlocksRecoveryWithoutMutatingProfile() {
	state, profile, source := s.rotatedFixture()
	r := s.begin(profile, source)
	s.mutate(source, profile)
	s.Require().NoError(os.WriteFile(filepath.Join(state, "dotm", sourcePointerName),
		[]byte(strings.Repeat("b", 32)+"\n"), 0o600))
	s.Error(recoverJournal(context.Background(), r.path, io.Discard))
	s.Equal("advanced flake\n", mustRead(s.T(), filepath.Join(source, "flake.nix")))
	link, err := os.Readlink(profile)
	s.Require().NoError(err)
	s.Equal("profile-2-link", link)
	s.FileExists(r.path)
}

func (s *UpdateRecoverySuite) TestLegacyPendingJournalStillRestoresOriginalSource() {
	_, profile, source := s.fixture()
	r := s.begin(profile, source)
	r.data.Version = 1
	r.data.Source = ""
	data, err := json.Marshal(r.data)
	s.Require().NoError(err)
	s.Require().NoError(os.WriteFile(r.path, data, 0o600))
	s.mutate(source, profile)
	s.Require().NoError(recoverJournal(context.Background(), r.path, io.Discard))
	s.assertOriginal(source, profile)
}

func (s *UpdateRecoverySuite) TestFailedUpdateRestoresRotatedSourceAndPackages() {
	state, profile, legacy, desired := s.updateFixture(false)
	id := strings.Repeat("c", 32)
	source := filepath.Join(state, "dotm", "sources", id)
	s.Require().NoError(os.MkdirAll(filepath.Dir(source), 0o700))
	s.Require().NoError(os.Rename(legacy, source))
	s.Require().NoError(os.WriteFile(filepath.Join(state, "dotm", sourcePointerName), []byte(id+"\n"), 0o600))
	for _, generation := range []string{"generation-1", "generation-2"} {
		path := filepath.Join(filepath.Dir(state), generation, "manifest.json")
		data, err := os.ReadFile(path)
		s.Require().NoError(err)
		data = bytes.ReplaceAll(data, []byte(legacy), []byte(source))
		// #nosec G703 -- Fixed generation names under this test's private temporary directory.
		s.Require().NoError(os.WriteFile(path, data, 0o600))
	}
	s.T().Setenv("FAKE_LOCK", filepath.Join(source, "flake.lock"))
	s.T().Setenv("FAIL_ADD", "fail")
	desired.Applications[0].Packages = append(desired.Applications[0].Packages, manifest.Package{Attr: "jq"})
	desired.Packages = append(desired.Packages, manifest.Package{Attr: "jq"})
	report, err := Update(context.Background(), desired, io.Discard, ConfigurationOptions{})
	s.Require().Error(err)
	s.Equal("partial", report.Applications[0].State)
	s.assertOriginal(source, profile)
	s.NoDirExists(legacy)
	s.assertJournalCleared(state)
}
