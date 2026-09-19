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

type MutationSuite struct{ suite.Suite }

func TestMutationSuite(t *testing.T) { suite.Run(t, new(MutationSuite)) }

func (s *MutationSuite) TestOwnershipReleaseAndCombinedContext() {
	s.T().Setenv("XDG_STATE_HOME", filepath.Join(s.T().TempDir(), "state"))
	ctx, release, err := BeginMutation(context.Background())
	s.Require().NoError(err)
	s.T().Cleanup(release)
	_, _, err = BeginMutation(context.Background())
	s.ErrorIs(err, errMutationBusy)

	_, releaseInner, err := BeginMutation(ctx)
	s.Require().NoError(err)
	releaseInner()
	_, _, err = BeginMutation(context.Background())
	s.ErrorIs(err, errMutationBusy, "an individual step must not release combined ownership")

	release()
	_, releaseNext, err := BeginMutation(context.Background())
	s.Require().NoError(err)
	s.T().Cleanup(releaseNext)
	_, _, err = BeginMutation(ctx)
	s.ErrorIs(err, errMutationBusy, "a released context must not authorize a later operation")
}

func (s *MutationSuite) TestAliasesContendButIndependentStateDoesNot() {
	root := s.T().TempDir()
	real := filepath.Join(root, "real")
	alias := filepath.Join(root, "alias")
	s.Require().NoError(os.Mkdir(real, 0o700))
	s.Require().NoError(os.Symlink(real, alias))
	s.T().Setenv("XDG_STATE_HOME", filepath.Join(real, "missing", "state"))
	_, release, err := BeginMutation(context.Background())
	s.Require().NoError(err)
	s.T().Cleanup(release)

	s.T().Setenv("XDG_STATE_HOME", filepath.Join(alias, "missing", "state"))
	_, _, err = BeginMutation(context.Background())
	s.ErrorIs(err, errMutationBusy)

	s.T().Setenv("XDG_STATE_HOME", filepath.Join(real, "independent"))
	_, releaseIndependent, err := BeginMutation(context.Background())
	s.Require().NoError(err)
	s.T().Cleanup(releaseIndependent)
	_, err = os.Lstat(filepath.Join(real, "missing"))
	s.ErrorIs(err, os.ErrNotExist, "coordination must not initialize XDG state")
}

func (s *MutationSuite) TestMutationEntrypointsRejectBeforeInspection() {
	base := filepath.Join(s.T().TempDir(), "state")
	s.T().Setenv("XDG_STATE_HOME", base)
	s.T().Setenv("PATH", "")
	_, release, err := BeginMutation(context.Background())
	s.Require().NoError(err)
	s.T().Cleanup(release)

	operations := []struct {
		name string
		run  func() error
	}{
		{"install", func() error {
			_, operationErr := Install(context.Background(), manifest.Resolved{}, io.Discard)
			return operationErr
		}},
		{"update", func() error {
			_, operationErr := Update(context.Background(), manifest.Resolved{}, io.Discard)
			return operationErr
		}},
		{"remove-extras", func() error {
			_, operationErr := RemoveExtras(context.Background(), nil, manifest.Resolved{}, true, nil, io.Discard)
			return operationErr
		}},
		{"clean", func() error {
			return Clean(context.Background(), CleanOptions{Yes: true}, nil, io.Discard, io.Discard)
		}},
	}
	// If inspection happens before exclusion, this malformed profile returns a
	// different error. Contenders must not inspect or authorize a stale plan.
	s.Require().NoError(os.MkdirAll(filepath.Join(base, "nix"), 0o700))
	s.Require().NoError(os.WriteFile(filepath.Join(base, "nix", "profile"), nil, 0o600))
	for _, operation := range operations {
		s.Run(operation.name, func() { s.ErrorIs(operation.run(), errMutationBusy) })
	}
}

func (s *MutationSuite) TestReadOnlyAndDryRunRemainAvailable() {
	base := filepath.Join(s.T().TempDir(), "state")
	s.T().Setenv("XDG_STATE_HOME", base)
	s.T().Setenv("PATH", "")
	_, release, err := BeginMutation(context.Background())
	s.Require().NoError(err)
	s.T().Cleanup(release)
	_, err = Inspect(context.Background(), manifest.Resolved{})
	s.Require().NoError(err)
	s.Require().NoError(Clean(
		context.Background(), CleanOptions{DryRun: true}, nil, io.Discard, io.Discard,
	))
	_, err = os.Lstat(base)
	s.ErrorIs(err, os.ErrNotExist)
}

func (s *MutationSuite) TestHookFailureReleasesOwnership() {
	base := filepath.Join(s.T().TempDir(), "state")
	s.T().Setenv("XDG_STATE_HOME", base)
	desired := manifest.Resolved{Applications: []manifest.Application{{
		ID: "broken", PreHook: filepath.Join(base, "missing-hook"),
	}}}
	_, err := Install(context.Background(), desired, io.Discard)
	s.Require().Error(err)
	_, release, err := BeginMutation(context.Background())
	s.Require().NoError(err)
	release()
}

func (s *MutationSuite) TestDanglingStateAncestorIsNotAnEmptyEnvironment() {
	root := s.T().TempDir()
	alias := filepath.Join(root, "dangling")
	s.Require().NoError(os.Symlink(filepath.Join(root, "missing"), alias))
	s.T().Setenv("XDG_STATE_HOME", filepath.Join(alias, "state"))
	_, _, err := BeginMutation(context.Background())
	s.Error(err)
}
