package installed

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dotm/internal/manifest"

	"github.com/stretchr/testify/suite"
)

type RemoveSuite struct{ suite.Suite }

func TestRemoveSuite(t *testing.T) { suite.Run(t, new(RemoveSuite)) }

func (s *RemoveSuite) TestSharedEntriesRequireEveryApplicationApproval() {
	catalog := &manifest.Data{Apps: map[string]manifest.Application{
		"base":      {ID: "base", Dependencies: []string{"companion"}, Packages: []manifest.Package{{Attr: "hello"}}},
		"companion": {ID: "companion", Packages: []manifest.Package{{Attr: "which"}}},
		"alpha":     {ID: "alpha", Packages: []manifest.Package{{Attr: "which"}, {Attr: "jq"}, {Attr: "missing"}}},
		"beta":      {ID: "beta", Packages: []manifest.Package{{Attr: "jq"}, {Attr: "gnused"}}},
	}}
	desired, err := catalog.Resolve([]string{"base"})
	s.Require().NoError(err)
	before := classify(Report{Entries: []Entry{
		{Name: "hello-1", Active: true, Descriptor: "hello"},
		{Name: "hello-2", Active: true, Descriptor: "hello"},
		{Name: "which", Active: true, Descriptor: "which"},
		{Name: "jq", Active: true, Descriptor: "jq"},
		{Name: "sed", Active: true, Descriptor: "gnused"},
	}}, desired)
	units := removalUnits(before, catalog, desired)
	s.Require().Len(units, 2)
	s.Equal("alpha", units[0].application)
	s.Equal([]Entry{{Name: "jq", Active: true, Descriptor: "jq"}}, units[0].entries, "partial application excludes desired shared components")
	for _, tc := range []struct {
		name    string
		answers string
		want    []string
	}{
		{"reject first", "n\ny\n", []string{"sed"}},
		{"reject second", "y\nn\n", nil},
		{"approve both", "yes\ny\n", []string{"jq", "sed"}},
	} {
		s.Run(tc.name, func() {
			names, err := confirmRemovals(units, false, strings.NewReader(tc.answers), io.Discard)
			s.Require().NoError(err)
			s.Equal(tc.want, names)
		})
	}
}

func (s *RemoveSuite) TestUnmappedAndInactiveDesiredEntriesAreIndividualUnits() {
	catalog := &manifest.Data{Apps: map[string]manifest.Application{
		"base": {ID: "base", Packages: []manifest.Package{{Attr: "hello"}}},
	}}
	desired, err := catalog.Resolve([]string{"base"})
	s.Require().NoError(err)
	before := classify(Report{Entries: []Entry{
		{Name: "active", Active: true, Descriptor: "hello"},
		{Name: "inactive", Descriptor: "hello"},
		{Name: "removed-from-catalog", Active: true, Descriptor: "jq^bin,man"},
		{Name: "unknown", Active: true},
	}}, desired)
	units := removalUnits(before, catalog, desired)
	s.Require().Len(units, 3)
	for _, unit := range units {
		s.Empty(unit.application)
		s.Len(unit.entries, 1)
	}
	names, err := confirmRemovals(units, false, strings.NewReader("y\nn\ny\n"), io.Discard)
	s.Require().NoError(err)
	s.Equal([]string{"inactive", "unknown"}, names)
}

func (s *RemoveSuite) TestCancellationNeverReturnsEarlierApprovals() {
	units := []removalUnit{
		{application: "first", entries: []Entry{{Name: "one"}}},
		{application: "second", entries: []Entry{{Name: "two"}}},
	}
	for _, input := range []string{"y\nq\n", "y\n", "y\ny", ""} {
		s.Run(input, func() {
			names, err := confirmRemovals(units, false, strings.NewReader(input), io.Discard)
			s.Error(err)
			s.Nil(names)
		})
	}
	var out bytes.Buffer
	names, err := confirmRemovals(units, false, strings.NewReader("invalid\n\n NO \n"), &out)
	s.Require().NoError(err)
	s.Empty(names, "invalid answers reprompt; empty answers and case-insensitive no reject")
}

func (s *RemoveSuite) TestExplicitBypassDoesNotConsultInput() {
	units := []removalUnit{{application: "tools", entries: []Entry{{Name: "jq"}, {Name: "sed"}}}}
	names, err := confirmRemovals(units, true, strings.NewReader("q\n"), io.Discard)
	s.Require().NoError(err)
	s.Equal([]string{"jq", "sed"}, names)
}

func (s *RemoveSuite) TestAbsentProfileDoesNotCreateStateOrRequireNix() {
	base := filepath.Join(s.T().TempDir(), "state")
	s.T().Setenv("XDG_STATE_HOME", base)
	s.T().Setenv("PATH", "")
	r, err := RemoveExtras(context.Background(), &manifest.Data{}, manifest.Resolved{}, false, strings.NewReader(""), io.Discard)
	s.Require().NoError(err)
	s.Equal(filepath.Join(base, "nix", "profile"), r.Profile)
	_, err = os.Lstat(base)
	s.ErrorIs(err, os.ErrNotExist)
}
