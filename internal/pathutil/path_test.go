package pathutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"
)

type pathSuite struct {
	suite.Suite
	home string
}

func TestPathSuite(t *testing.T) { suite.Run(t, new(pathSuite)) }
func (s *pathSuite) SetupTest() {
	s.home = filepath.Join(s.T().TempDir(), "home space;[x]")
	s.T().Setenv("HOME", s.home)
	for _, key := range []string{
		"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "PATHUTIL_VALUE", "PATHUTIL_UNSET",
	} {
		s.T().Setenv(key, "")
	}
}

func (s *pathSuite) TestTargetXDGDefaultsAndNondefaults() {
	for _, tc := range []struct{ key, tail string }{
		{"XDG_CONFIG_HOME", ".config"},
		{"XDG_DATA_HOME", ".local/share"},
		{"XDG_STATE_HOME", ".local/state"},
		{"XDG_CACHE_HOME", ".cache"},
	} {
		s.Equal(filepath.Join(s.home, tc.tail, "app"), mustTarget(s.T(), "$"+tc.key+"/app"))
	}
	custom := filepath.Join(s.home, "custom data")
	s.T().Setenv("XDG_DATA_HOME", custom)
	s.Equal(filepath.Join(custom, "app"), mustTarget(s.T(), "${XDG_DATA_HOME}/app"))
}

func (s *pathSuite) TestTargetTildeAndLiteralMetacharacters() {
	want := filepath.Join(s.home, "a b;[x]{y}")
	s.Equal(want, mustTarget(s.T(), "~/a b;[x]{y}"))
	s.Equal(filepath.Join(s.home, "trailing"), mustTarget(s.T(), "~/trailing///"))
	s.T().Setenv("PATHUTIL_VALUE", "literal $PATHUTIL_UNSET `x` {z}")
	s.Equal(filepath.Join(s.home, "literal $PATHUTIL_UNSET `x` {z}"), mustTarget(s.T(), "$HOME/$PATHUTIL_VALUE"))
}

func (s *pathSuite) TestRejectInvalidTargets() {
	for _, value := range []string{
		"", "relative/path", "~someone/x", "$PATHUTIL_UNSET", "${PATHUTIL_UNSET}", "${BROKEN", "$",
		"$()", "$(echo x)", "`echo x`", "/nul\x00path",
	} {
		_, err := Target(value)
		s.Errorf(err, "expected rejection for %q", value)
	}
	s.T().Setenv("HOME", "")
	_, err := Target("~/x")
	s.Error(err)
}

func (s *pathSuite) TestTargetUnknownUnsetAndFallback() {
	s.Require().NoError(os.Unsetenv("PATHUTIL_UNSET"))
	_, err := Target("/${PATHUTIL_UNSET}")
	s.Error(err)
	s.Equal(filepath.Join(s.home, "fallback"), mustTarget(s.T(), "${PATHUTIL_UNSET:-$HOME/fallback}"))
}

func (s *pathSuite) TestHookPreservesRelativeAndExpansionSemantics() {
	dir := s.T().TempDir()
	s.Equal(filepath.Join(dir, "relative path", "hook"), mustHook(s.T(), "relative path/hook", dir))
	s.Equal(filepath.Join(s.home, "hook"), mustHook(s.T(), "~/hook", dir))
	s.T().Setenv("PATHUTIL_VALUE", "$PATHUTIL_UNSET")
	s.Equal(filepath.Join(dir, "$PATHUTIL_UNSET", "hook"), mustHook(s.T(), "$PATHUTIL_VALUE/hook", dir))
}

func mustTarget(t *testing.T, value string) string {
	t.Helper()
	got, err := Target(value)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func mustHook(t *testing.T, value, dir string) string {
	t.Helper()
	got, err := Hook(value, dir)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func (s *pathSuite) TestTargetRuntimeAndInvalidHomeBoundaries() {
	s.T().Setenv("XDG_RUNTIME_DIR", "")
	s.Require().NoError(os.Unsetenv("XDG_RUNTIME_DIR"))
	_, err := Target("$XDG_RUNTIME_DIR/app")
	s.Error(err, "runtime directory must not receive an invented default")
	s.T().Setenv("XDG_CONFIG_HOME", "relative")
	_, err = Target("$XDG_CONFIG_HOME/app")
	s.Error(err)
	s.T().Setenv("HOME", "relative")
	_, err = Target("~/app")
	s.Error(err)
	s.T().Setenv("HOME", "")
	s.T().Setenv("XDG_DATA_HOME", "")
	_, err = Target("$XDG_DATA_HOME/app")
	s.Error(err, "XDG defaults require a usable HOME")
	s.T().Setenv("HOME", s.home)
	s.Equal("/custom/app", mustTarget(s.T(), "${XDG_DATA_HOME:-/custom}/app"))
}
