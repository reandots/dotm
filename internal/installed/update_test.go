package installed

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"dotm/internal/manifest"

	"github.com/stretchr/testify/suite"
)

type UpdateSuite struct{ suite.Suite }

func TestUpdateSuite(t *testing.T) { suite.Run(t, new(UpdateSuite)) }

func (s *UpdateSuite) TestTargetsPreserveExtrasAndInstallMissingCompanions() {
	const source = "/state/dotm/source"
	desired := manifest.Resolved{
		Applications: []manifest.Application{
			{ID: "companion", Packages: []manifest.Package{{Attr: "which"}}},
			{ID: "tool", Packages: []manifest.Package{{Attr: "hello"}, {Attr: "jq", Outputs: []string{"bin", "man"}}}},
		},
		Packages: []manifest.Package{{Attr: "hello"}, {Attr: "jq", Outputs: []string{"bin", "man"}}, {Attr: "which"}},
	}
	before := classify(Report{Entries: []Entry{
		{Name: "tool", Active: true, Descriptor: "hello", OriginalURL: "path:" + source, Updateable: true},
		{Name: "json", Active: true, Descriptor: "jq^bin,man", OriginalURL: "path:" + source, Updateable: true},
		{Name: "inactive-companion", Active: false, Descriptor: "which", OriginalURL: "path:" + source, Updateable: true},
		{Name: "removed-from-catalog", Active: true, Descriptor: "gnused", OriginalURL: "path:" + source, Updateable: true},
		{Name: "unknown", Active: true},
	}}, desired)

	names, err := updateTargets(before, desired, source)
	s.Require().NoError(err)
	s.Equal([]string{"json", "tool"}, names)
	missing, retained, allowInit, err := installationPlan(before, desired, source)
	s.Require().NoError(err)
	s.Equal([]manifest.Package{{Attr: "which"}}, missing)
	s.Contains(retained, manifest.Package{Attr: "gnused"})
	s.False(allowInit)
}

func (s *UpdateSuite) TestForeignDesiredRecipeCannotPretendToFollowLocalLock() {
	desired := manifest.Resolved{Packages: []manifest.Package{{Attr: "hello"}}}
	before := Report{Entries: []Entry{
		{Name: "foreign", Active: true, Descriptor: "hello", OriginalURL: "github:NixOS/nixpkgs/pinned-revision", Updateable: true},
	}}
	names, err := updateTargets(before, desired, "/source")
	s.Error(err)
	s.Empty(names)
}

func (s *UpdateSuite) TestEmptyDesiredSetLeavesAbsentEnvironmentUntouched() {
	base := filepath.Join(s.T().TempDir(), "state")
	s.T().Setenv("XDG_STATE_HOME", base)
	s.T().Setenv("PATH", "")
	report, err := Update(context.Background(), manifest.Resolved{}, io.Discard)
	s.Require().NoError(err)
	s.Equal(filepath.Join(base, "nix", "profile"), report.Profile)
	_, err = os.Lstat(base)
	s.ErrorIs(err, os.ErrNotExist)
}

func (s *UpdateSuite) TestSourceMetadataAloneIsNotAPackageChange() {
	before := Report{Entries: []Entry{{Name: "tool", URL: "path:/source?narHash=old", StorePaths: []string{"/nix/store/hello"}}}}
	after := Report{Entries: []Entry{{Name: "tool", URL: "path:/source?narHash=new", StorePaths: []string{"/nix/store/hello"}}}}
	s.False(packagePathsChanged(before, after))
	after.Entries[0].StorePaths = []string{"/nix/store/new-hello"}
	s.True(packagePathsChanged(before, after))
}
