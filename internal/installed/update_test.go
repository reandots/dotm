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

func (s *UpdateSuite) TestTargetsCompleteInstalledApplicationsButSkipNewRoots() {
	const source = "/state/dotm/source"
	desired := manifest.Resolved{
		Applications: []manifest.Application{
			{ID: "leaf", Packages: []manifest.Package{{Attr: "xz"}}},
			{ID: "companion", Dependencies: []string{"leaf"}, Packages: []manifest.Package{{Attr: "which"}}},
			{ID: "tool", Dependencies: []string{"companion"}, Packages: []manifest.Package{{Attr: "hello"}, {Attr: "jq", Outputs: []string{"bin", "man"}}, {Attr: "fd"}}},
			{ID: "new-app", Packages: []manifest.Package{{Attr: "ripgrep"}}},
		},
		Packages: []manifest.Package{{Attr: "fd"}, {Attr: "hello"}, {Attr: "jq", Outputs: []string{"bin", "man"}}, {Attr: "ripgrep"}, {Attr: "which"}, {Attr: "xz"}},
	}
	before := classify(Report{Entries: []Entry{
		{Name: "tool", Active: true, Descriptor: "hello", OriginalURL: "path:" + source, Updateable: true},
		{Name: "json", Active: true, Descriptor: "jq^bin,man", OriginalURL: "path:" + source, Updateable: true},
		{Name: "inactive-companion", Active: false, Descriptor: "which", OriginalURL: "path:" + source, Updateable: true},
		{Name: "removed-from-catalog", Active: true, Descriptor: "gnused", OriginalURL: "path:" + source, Updateable: true},
		{Name: "unknown", Active: true},
	}}, desired)
	selected := updateSelection(before, desired)
	names, err := updateTargets(before, selected, source)
	s.Require().NoError(err)
	s.Equal([]string{"json", "tool"}, names)
	missing, retained, allowInit, err := installationPlan(before, selected, source)
	s.Require().NoError(err)
	s.Equal([]manifest.Package{{Attr: "fd"}, {Attr: "which"}, {Attr: "xz"}}, missing)
	s.NotContains(retained, manifest.Package{Attr: "ripgrep"})
	s.Contains(retained, manifest.Package{Attr: "gnused"})
	s.False(allowInit)
}

func (s *UpdateSuite) TestInstalledDependencyDoesNotSelectMissingParentOrInactiveApplication() {
	desired := manifest.Resolved{
		Applications: []manifest.Application{
			{ID: "companion", Packages: []manifest.Package{{Attr: "which"}}},
			{ID: "new-parent", Dependencies: []string{"companion"}, Packages: []manifest.Package{{Attr: "hello"}}},
			{ID: "inactive", Packages: []manifest.Package{{Attr: "jq"}}},
		},
		Packages: []manifest.Package{{Attr: "hello"}, {Attr: "jq"}, {Attr: "which"}},
	}
	before := classify(Report{Entries: []Entry{
		{Name: "companion", Active: true, Descriptor: "which"},
		{Name: "inactive", Active: false, Descriptor: "jq"},
	}}, desired)
	selected := updateSelection(before, desired)
	s.Equal([]manifest.Package{{Attr: "which"}}, selected.Packages)
}

func (s *UpdateSuite) TestSharedActiveDescriptorCountsAsPartialInstallation() {
	desired := manifest.Resolved{
		Applications: []manifest.Application{
			{ID: "existing", Packages: []manifest.Package{{Attr: "hello"}}},
			{ID: "shared", Packages: []manifest.Package{{Attr: "hello"}, {Attr: "which"}}},
		},
		Packages: []manifest.Package{{Attr: "hello"}, {Attr: "which"}},
	}
	before := classify(Report{Entries: []Entry{{Name: "tool", Active: true, Descriptor: "hello"}}}, desired)
	selected := updateSelection(before, desired)
	missing, _, _, err := installationPlan(before, selected, "/source")
	s.Require().NoError(err)
	s.Equal([]manifest.Package{{Attr: "which"}}, missing)
}

func (s *UpdateSuite) TestAmbiguousInstalledApplicationStillRefusesMutation() {
	desired := manifest.Resolved{
		Applications: []manifest.Application{{ID: "tool", Packages: []manifest.Package{{Attr: "hello"}, {Attr: "which"}}}},
		Packages:     []manifest.Package{{Attr: "hello"}, {Attr: "which"}},
	}
	before := classify(Report{Entries: []Entry{
		{Name: "first", Active: true, Descriptor: "hello"},
		{Name: "second", Active: true, Descriptor: "hello"},
	}}, desired)
	selected := updateSelection(before, desired)
	_, _, _, err := installationPlan(before, selected, "/source")
	s.Error(err)
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

func (s *UpdateSuite) TestNewDesiredApplicationsLeaveAbsentEnvironmentUntouched() {
	base := filepath.Join(s.T().TempDir(), "state")
	s.T().Setenv("XDG_STATE_HOME", base)
	s.T().Setenv("PATH", "")
	desired := manifest.Resolved{
		Applications: []manifest.Application{{ID: "new", Packages: []manifest.Package{{Attr: "hello"}}}},
		Packages:     []manifest.Package{{Attr: "hello"}},
	}
	report, err := Update(context.Background(), desired, io.Discard)
	s.Require().NoError(err)
	s.Require().Len(report.Applications, 1)
	s.Equal("missing", report.Applications[0].State)
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
