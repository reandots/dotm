package installed

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"
)

type CleanSuite struct{ suite.Suite }

func TestCleanSuite(t *testing.T) { suite.Run(t, new(CleanSuite)) }

func (s *CleanSuite) makeHistory(current int, numbers ...int) string {
	s.T().Helper()
	base := s.T().TempDir()
	s.T().Setenv("XDG_STATE_HOME", base)
	s.T().Setenv("PATH", "")
	profile := filepath.Join(base, "nix", "profile")
	s.Require().NoError(os.MkdirAll(filepath.Dir(profile), 0o700))
	for _, n := range numbers {
		s.addGeneration(profile, n)
	}
	s.Require().NoError(os.Symlink("profile-"+strconv.Itoa(current)+"-link", profile))
	return profile
}

func (s *CleanSuite) addGeneration(profile string, n int) {
	s.T().Helper()
	target := s.T().TempDir()
	s.Require().NoError(os.WriteFile(filepath.Join(target, "manifest.json"), []byte(`{"version":3,"elements":{}}`), 0o600))
	s.Require().NoError(os.Symlink(target, profile+"-"+strconv.Itoa(n)+"-link"))
}

func (s *CleanSuite) TestRetentionPreservesActiveAcrossRollback() {
	for _, tc := range []struct {
		name     string
		current  int
		keep     int
		retained []int
		selected []int
	}{
		{"latest", 7, 2, []int{2, 3, 7}, []int{1}},
		{"rolled-back", 2, 2, []int{2, 3, 7}, []int{1}},
		{"oldest-active", 1, 2, []int{1, 3, 7}, []int{2}},
		{"only-active", 2, 0, []int{2}, []int{1, 3, 7}},
		{"retain-all", 2, int(^uint(0) >> 1), []int{1, 2, 3, 7}, nil},
	} {
		s.Run(tc.name, func() {
			profile := s.makeHistory(tc.current, 1, 2, 3, 7)
			h, err := readHistory(profile)
			s.Require().NoError(err)
			retained, selected := planHistory(h, tc.keep)
			s.Equal(tc.retained, retained)
			s.Equal(tc.selected, selected)
		})
	}
}

func (s *CleanSuite) TestRejectsInvalidHistoryWithoutMutation() {
	for _, name := range []string{"corrupt-manifest", "unsupported-manifest", "dangling-retired", "noncanonical-number", "foreign-history", "orphaned-history"} {
		s.Run(name, func() {
			profile := s.makeHistory(2, 1, 2)
			switch name {
			case "corrupt-manifest", "unsupported-manifest":
				target, err := filepath.EvalSymlinks(profile + "-1-link")
				s.Require().NoError(err)
				data := []byte("{")
				if name == "unsupported-manifest" {
					data = []byte(`{"version":4,"elements":{}}`)
				}
				s.Require().NoError(os.WriteFile(filepath.Join(target, "manifest.json"), data, 0o600))
			case "dangling-retired":
				s.Require().NoError(os.Remove(profile + "-1-link"))
				s.Require().NoError(os.Symlink(filepath.Join(s.T().TempDir(), "absent"), profile+"-1-link"))
			case "noncanonical-number":
				s.Require().NoError(os.Symlink(profile+"-1-link", profile+"-01-link"))
			case "foreign-history":
				other := s.makeHistory(2, 1, 2)
				s.T().Setenv("XDG_STATE_HOME", filepath.Dir(filepath.Dir(profile)))
				s.Require().NoError(os.Remove(profile))
				s.Require().NoError(os.Symlink(other+"-2-link", profile))
			case "orphaned-history":
				s.Require().NoError(os.Remove(profile))
			}
			err := Clean(context.Background(), CleanOptions{Keep: 0, Yes: true, GC: true}, strings.NewReader(""), io.Discard, io.Discard)
			s.Error(err)
			_, err = os.Lstat(profile + "-1-link")
			s.NoError(err, "invalid state must not prune retired history")
		})
	}
}

func (s *CleanSuite) TestAbsentProfileNeedsNoNixOrState() {
	base := filepath.Join(s.T().TempDir(), "absent")
	s.T().Setenv("XDG_STATE_HOME", base)
	s.T().Setenv("PATH", "")
	s.Require().NoError(Clean(context.Background(), CleanOptions{}, strings.NewReader(""), io.Discard, io.Discard))
	_, err := os.Lstat(base)
	s.ErrorIs(err, os.ErrNotExist)
}

func (s *CleanSuite) TestCancellationLeavesHistoryUntouched() {
	for _, tc := range []struct {
		answer string
		fails  bool
	}{{"n\n", false}, {"\n", false}, {"q\n", true}, {"", true}, {"yes", true}, {"invalid\nno\n", false}} {
		s.Run(strconv.Quote(tc.answer), func() {
			profile := s.makeHistory(3, 1, 2, 3)
			before, err := readHistory(profile)
			s.Require().NoError(err)
			err = Clean(context.Background(), CleanOptions{Keep: 0, GC: true}, strings.NewReader(tc.answer), io.Discard, io.Discard)
			if tc.fails {
				s.Error(err)
			} else {
				s.NoError(err)
			}
			after, err := readHistory(profile)
			s.Require().NoError(err)
			s.Equal(before, after)
		})
	}
}

type cleanupAnswer struct {
	io.Reader
	change func()
}

func (r *cleanupAnswer) Read(p []byte) (int, error) {
	if r.change != nil {
		r.change()
		r.change = nil
	}
	return r.Reader.Read(p)
}

func (s *CleanSuite) TestChangedHistoryInvalidatesApproval() {
	for _, name := range []string{"current", "retired-target", "new-generation", "absent-to-present"} {
		s.Run(name, func() {
			profile := s.makeHistory(3, 1, 2, 3)
			if name == "absent-to-present" {
				for _, suffix := range []string{"", "-1-link", "-2-link", "-3-link"} {
					s.Require().NoError(os.Remove(profile + suffix))
				}
			}
			answer := &cleanupAnswer{Reader: strings.NewReader("yes\n"), change: func() {
				switch name {
				case "current":
					s.Require().NoError(os.Remove(profile))
					s.Require().NoError(os.Symlink("profile-2-link", profile))
				case "retired-target":
					s.Require().NoError(os.Remove(profile + "-1-link"))
					s.Require().NoError(os.Symlink(profile+"-2-link", profile+"-1-link"))
				case "new-generation":
					s.addGeneration(profile, 4)
				case "absent-to-present":
					s.addGeneration(profile, 1)
					s.Require().NoError(os.Symlink("profile-1-link", profile))
				}
			}}
			err := Clean(context.Background(), CleanOptions{Keep: 0, GC: true}, answer, io.Discard, io.Discard)
			s.ErrorContains(err, "profile history changed")
			_, err = os.Lstat(profile + "-1-link")
			s.NoError(err, "stale approval must not prune or launch GC")
		})
	}
}

type failedCleanOutput struct{}

func (failedCleanOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func (s *CleanSuite) TestUnreadablePlanCannotAuthorizeDeletion() {
	profile := s.makeHistory(2, 1, 2)
	err := Clean(context.Background(), CleanOptions{Keep: 0, Yes: true}, strings.NewReader(""), failedCleanOutput{}, io.Discard)
	s.ErrorIs(err, io.ErrClosedPipe)
	_, err = os.Lstat(profile + "-1-link")
	s.NoError(err)
}
