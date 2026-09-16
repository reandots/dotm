package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"
)

type pathSuite struct {
	suite.Suite
	dir string
}

func TestPathSuite(t *testing.T) { suite.Run(t, new(pathSuite)) }

func (s *pathSuite) SetupTest() {
	s.dir = s.T().TempDir()
	s.T().Setenv("HOME", filepath.Join(s.dir, "home"))
	s.T().Setenv("XDG_DATA_HOME", "")
	s.T().Setenv("HOOK_TEST_VALUE", "")
	s.T().Setenv("UNSET_OTHER", "")
	s.T().Setenv("UNSET_FALLBACK", "")
}

func (s *pathSuite) TestAbsoluteRelativeAndSpaces() {
	s.Equal(filepath.Join(s.dir, "relative path", "hook"), mustResolve(s.T(), "relative path/hook", s.dir))
	s.Equal(filepath.Join(s.dir, "absolute path", "hook"), mustResolve(s.T(), filepath.Join(s.dir, "absolute path", "hook"), "/ignored"))
}

func (s *pathSuite) TestTildeOnlyAtStart() {
	s.Equal(filepath.Join(s.dir, "home", "bin", "hook"), mustResolve(s.T(), "~/bin/hook", s.dir))
	s.Equal(filepath.Join(s.dir, "home"), mustResolve(s.T(), "~", s.dir))
	_, err := resolvePath("~someone/hook", s.dir)
	s.Error(err)
}

func (s *pathSuite) TestNestedFallbackChosenAndSkipped() {
	s.Require().NoError(os.Unsetenv("XDG_DATA_HOME"))
	s.Equal(filepath.Join(s.dir, "home", ".local", "share", "app"), mustResolve(s.T(), "${XDG_DATA_HOME:-$HOME/.local/share}/app", s.dir))
	s.Require().NoError(os.Setenv("XDG_DATA_HOME", filepath.Join(s.dir, "data")))
	s.Require().NoError(os.Unsetenv("UNSET_FALLBACK"))
	s.Equal(filepath.Join(s.dir, "data", "app"), mustResolve(s.T(), "${XDG_DATA_HOME:-$UNSET_FALLBACK}/app", s.dir))
	s.Require().NoError(os.Unsetenv("XDG_DATA_HOME"))
	s.Equal(filepath.Join(s.dir, "nested"), mustResolve(s.T(), "${XDG_DATA_HOME:-${UNSET_FALLBACK:-nested}}", s.dir))
}

func (s *pathSuite) TestTildeHomeIsLiteral() {
	home := filepath.Join(s.dir, "$UNSET_OTHER`literal`home")
	s.T().Setenv("HOME", home)
	s.Equal(filepath.Join(home, "hook"), mustResolve(s.T(), "~/hook", s.dir))
}

func (s *pathSuite) TestUnsetEmptyAndFallback() {
	s.Require().NoError(os.Unsetenv("HOOK_TEST_VALUE"))
	_, err := resolvePath("$HOOK_TEST_VALUE", s.dir)
	s.Error(err)
	s.Equal(filepath.Join(s.dir, "fallback"), mustResolve(s.T(), "${HOOK_TEST_VALUE:-fallback}", s.dir))
	s.Require().NoError(os.Setenv("HOOK_TEST_VALUE", ""))
	s.Equal(filepath.Join(s.dir, "fallback"), mustResolve(s.T(), "${HOOK_TEST_VALUE:-fallback}", s.dir))
	s.Equal(filepath.Join(s.dir, "prefix", "suffix"), mustResolve(s.T(), "prefix$HOOK_TEST_VALUE/suffix", s.dir))
}

func (s *pathSuite) TestEnvironmentValuesAreNotReexpanded() {
	s.Require().NoError(os.Setenv("HOOK_TEST_VALUE", "$UNSET_OTHER"))
	s.Equal(filepath.Join(s.dir, "$UNSET_OTHER", "hook"), mustResolve(s.T(), "$HOOK_TEST_VALUE/hook", s.dir))
}

func (s *pathSuite) TestRejectMalformedAndSubstitution() {
	for _, value := range []string{"${BROKEN", "${X:=y}", "${X:+y}", "${}", "$", "$()", "$(touch /tmp/no)", "`touch /tmp/no`"} {
		_, err := resolvePath(value, s.dir)
		s.Errorf(err, "expected rejection for %q", value)
	}
}

func mustResolve(t *testing.T, value, dir string) string {
	t.Helper()
	got, err := resolvePath(value, dir)
	if err != nil {
		t.Fatal(err)
	}
	return got
}
