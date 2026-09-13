package installed

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"dotm/internal/manifest"

	"github.com/stretchr/testify/suite"
)

type InstallSuite struct{ suite.Suite }

func TestInstallSuite(t *testing.T) { suite.Run(t, new(InstallSuite)) }

func (s *InstallSuite) TestPlanRetainsRemovedAndInactiveDescriptors() {
	const source = "/state/dotm/source"
	desired := manifest.Resolved{
		Applications: []manifest.Application{{ID: "tools", Packages: []manifest.Package{{Attr: "hello"}, {Attr: "jq", Outputs: []string{"bin", "man"}}}}},
		Packages:     []manifest.Package{{Attr: "hello"}, {Attr: "jq", Outputs: []string{"bin", "man"}}},
	}
	before := classify(Report{Entries: []Entry{
		{Name: "hello", Active: true, Descriptor: "hello", OriginalURL: "path:" + source},
		{Name: "removed", Active: true, Descriptor: "which", OriginalURL: "path:" + source},
		{Name: "inactive", Active: false, Descriptor: "jq^bin,man", OriginalURL: "path:" + source},
		{Name: "foreign", Active: true, OriginalURL: "github:someone/tool"},
	}}, desired)
	missing, retained, allowInit, err := installationPlan(before, desired, source)
	s.Require().NoError(err)
	s.Equal([]manifest.Package{{Attr: "jq", Outputs: []string{"bin", "man"}}}, missing)
	s.Equal([]manifest.Package{{Attr: "hello"}, {Attr: "jq", Outputs: []string{"bin", "man"}}, {Attr: "which"}}, retained)
	s.False(allowInit, "native source users prohibit silently repinning a lost lock")
}

func (s *InstallSuite) TestAmbiguityRefusesEvenUnrelatedMissingPackages() {
	desired := manifest.Resolved{
		Applications: []manifest.Application{{ID: "tools", Packages: []manifest.Package{{Attr: "hello"}, {Attr: "which"}}}},
		Packages:     []manifest.Package{{Attr: "hello"}, {Attr: "which"}},
	}
	before := classify(Report{Entries: []Entry{
		{Name: "first", Active: true, Descriptor: "hello"},
		{Name: "second", Active: true, Descriptor: "hello"},
	}}, desired)
	missing, _, _, err := installationPlan(before, desired, "/source")
	s.Error(err)
	s.Empty(missing, "do not mutate an ambiguous environment")
}

func (s *InstallSuite) TestUnrecognizedSourceEntryCannotBeErasedByRegeneration() {
	_, _, _, err := installationPlan(Report{Entries: []Entry{{Name: "foreign-output", OriginalURL: "path:/source"}}}, manifest.Resolved{}, "/source")
	s.Error(err)
}

func (s *InstallSuite) TestEmptyDesiredSetDoesNotInitializeStateOrRequireNix() {
	base := filepath.Join(s.T().TempDir(), "state")
	s.T().Setenv("XDG_STATE_HOME", base)
	s.T().Setenv("PATH", "")
	r, err := Install(context.Background(), manifest.Resolved{}, os.Stderr)
	s.Require().NoError(err)
	s.Equal(filepath.Join(base, "nix", "profile"), r.Profile)
	_, err = os.Lstat(base)
	s.ErrorIs(err, os.ErrNotExist)
}
