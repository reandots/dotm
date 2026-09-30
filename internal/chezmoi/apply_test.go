package chezmoi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"dotm/internal/manifest"
	"github.com/stretchr/testify/suite"
)

type applySuite struct {
	suite.Suite
	root   string
	source string
	dest   string
}

func TestApplySuite(t *testing.T) {
	if _, err := exec.LookPath("chezmoi"); err != nil {
		t.Skip("chezmoi is required for real-backend apply tests")
	}
	suite.Run(t, new(applySuite))
}

func (s *applySuite) SetupTest() {
	s.root = s.T().TempDir()
	s.source = filepath.Join(s.root, "source")
	s.dest = filepath.Join(s.root, "home")
	for _, path := range []string{s.source, s.dest} {
		s.Require().NoError(os.MkdirAll(path, 0o700))
	}
	s.T().Setenv("HOME", s.dest)
	s.T().Setenv("XDG_CONFIG_HOME", filepath.Join(s.root, "xdg-config"))
	s.T().Setenv("XDG_CONFIG_DIRS", filepath.Join(s.root, "xdg-system-config"))
	s.T().Setenv("XDG_DATA_HOME", filepath.Join(s.root, "xdg-data"))
	s.T().Setenv("XDG_STATE_HOME", filepath.Join(s.root, "xdg-state"))
	s.T().Setenv("XDG_CACHE_HOME", filepath.Join(s.root, "xdg-cache"))
}

func (s *applySuite) put(rel, content string) {
	s.T().Helper()
	path := filepath.Join(s.source, rel)
	s.Require().NoError(os.MkdirAll(filepath.Dir(path), 0o700))
	s.Require().NoError(os.WriteFile(path, []byte(content), 0o600))
}

func (s *applySuite) apply(targets []manifest.ConfigTarget, blocked ...string) ApplyResult {
	s.T().Helper()
	for i := range targets {
		if !filepath.IsAbs(targets[i].Path) {
			targets[i].Path = filepath.Join(s.dest, targets[i].Path)
		}
	}
	result, err := Apply(context.Background(), s.source, targets, blocked)
	s.Require().NoError(err)
	return result
}

func (s *applySuite) read(rel string) string {
	s.T().Helper()
	data, err := os.ReadFile(filepath.Join(s.dest, rel))
	s.Require().NoError(err)
	return string(data)
}

func (s *applySuite) TestFirstAdoptionConflictPreservesUserDataAndAppliesSibling() {
	s.put("dot_config/app.conf", "managed\n")
	s.put("dot_config/sibling", "sibling\n")
	s.Require().NoError(os.MkdirAll(filepath.Join(s.dest, ".config"), 0o700))
	s.Require().NoError(os.WriteFile(filepath.Join(s.dest, ".config", "app.conf"), []byte("user\n"), 0o600))
	s.Require().NoError(os.WriteFile(filepath.Join(s.dest, ".config", "extra"), []byte("extra\n"), 0o600))
	target := []manifest.ConfigTarget{{Path: ".config", Applications: []string{"app"}}}
	first := s.apply(target)
	s.Equal("user\n", s.read(".config/app.conf"))
	s.Equal("extra\n", s.read(".config/extra"))
	s.Equal("sibling\n", s.read(".config/sibling"))
	s.True(hasEntryStatus(first, filepath.Join(s.dest, ".config", "app.conf"), ApplyConflict))
	second := s.apply(target)
	s.Equal("user\n", s.read(".config/app.conf"))
	s.Equal("extra\n", s.read(".config/extra"))
	s.Equal("sibling\n", s.read(".config/sibling"))
	s.True(hasEntryStatus(second, filepath.Join(s.dest, ".config", "app.conf"), ApplyConflict))
}

func (s *applySuite) TestManagedUpdateAndEditedOrLostStateConflict() {
	s.put("managed", "one\n")
	target := []manifest.ConfigTarget{{Path: "managed", Applications: []string{"app"}}}
	s.apply(target)
	s.put("managed", "two\n")
	updated := s.apply(target)
	s.Equal("two\n", s.read("managed"))
	s.True(hasEntryStatus(updated, filepath.Join(s.dest, "managed"), ApplySuccess))
	// A tracked file modified locally must not be overwritten.
	s.Require().NoError(os.WriteFile(filepath.Join(s.dest, "managed"), []byte("local edit\n"), 0o600))
	s.put("managed", "three\n")
	edited := s.apply(target)
	s.Equal("local edit\n", s.read("managed"))
	s.True(hasEntryStatus(edited, filepath.Join(s.dest, "managed"), ApplyConflict))
	// Removing chezmoi's state while leaving the adopted file recreates a first-adoption conflict.
	s.Require().NoError(os.WriteFile(filepath.Join(s.dest, "managed"), []byte("two\n"), 0o600))
	s.Require().NoError(os.Chmod(filepath.Join(s.dest, "managed"), 0o644))
	s.Require().NoError(os.Remove(filepath.Join(s.root, "xdg-config", "chezmoi", "chezmoistate.boltdb")))
	lost := s.apply(target)
	s.Equal("two\n", s.read("managed"))
	s.True(hasEntryStatus(lost, filepath.Join(s.dest, "managed"), ApplyConflict))
}

func (s *applySuite) TestNestedIgnoreAndMissingDistinction() {
	s.put("dot_config/ignored.db", "ignored\n")
	s.put("dot_config/visible", "visible\n")
	s.put("dot_config/.chezmoiignore", "*.db\n")
	result := s.apply([]manifest.ConfigTarget{
		{Path: ".config", Applications: []string{"app"}},
		{Path: ".config/ignored.db", Applications: []string{"ignored-owner"}},
		{Path: ".config/missing", Applications: []string{"missing-owner"}},
	})
	s.False(fileExists(filepath.Join(s.dest, ".config", "ignored.db")))
	s.Equal("visible\n", s.read(".config/visible"))
	s.Equal(ApplyIgnored, targetStatus(result, filepath.Join(s.dest, ".config", "ignored.db")))
	s.Equal(ApplyMissing, targetStatus(result, filepath.Join(s.dest, ".config", "missing")))
}

func (s *applySuite) TestSharedAndOverlappingOwnershipBlockedChildShielding() {
	s.put("dot_config/shared", "shared\n")
	s.put("dot_config/private", "private\n")
	targets := []manifest.ConfigTarget{
		{Path: ".config", Applications: []string{"broad"}},
		{Path: ".config/shared", Applications: []string{"healthy"}},
		{Path: ".config/private", Applications: []string{"blocked"}},
	}
	result := s.apply(targets, "blocked")
	s.Equal("shared\n", s.read(".config/shared"))
	s.False(fileExists(filepath.Join(s.dest, ".config", "private")))
	entry, ok := findEntry(result, filepath.Join(s.dest, ".config/shared"))
	s.True(ok)
	s.Contains(entry.Applications, "broad")
	s.Contains(entry.Applications, "healthy")
	s.Contains(entry.SelectedApplications, "healthy")
	blocked, ok := findEntry(result, filepath.Join(s.dest, ".config/private"))
	s.True(ok)
	s.Contains(blocked.Applications, "broad")
	s.Contains(blocked.Applications, "blocked")
	s.NotContains(blocked.SelectedApplications, "broad")
}

func (s *applySuite) TestSymlinkAncestorAndDirectoryReplacementPreserveAndSafeWrite() {
	s.put("dot_config/link/secret", "unsafe\n")
	s.put("dot_config/replace/child", "unsafe\n")
	s.put("dot_config/safe", "safe\n")
	config := filepath.Join(s.dest, ".config")
	s.Require().NoError(os.MkdirAll(config, 0o700))
	outside := filepath.Join(s.root, "outside")
	s.Require().NoError(os.MkdirAll(outside, 0o700))
	s.Require().NoError(os.Symlink(outside, filepath.Join(config, "link")))
	s.Require().NoError(os.WriteFile(filepath.Join(config, "replace"), []byte("user data"), 0o600))
	result := s.apply([]manifest.ConfigTarget{{Path: ".config", Applications: []string{"app"}}})
	s.False(fileExists(filepath.Join(outside, "secret")))
	s.Equal("user data", s.read(".config/replace"))
	s.Equal("safe\n", s.read(".config/safe"))
	s.True(hasEntryStatus(result, filepath.Join(config, "link", "secret"), ApplyConflict))
	s.True(hasEntryStatus(result, filepath.Join(config, "replace"), ApplyConflict))
}

func (s *applySuite) TestIdenticalContentAndPermissionOnlyAdoptionConflict() {
	s.put("file", "same\n")
	path := filepath.Join(s.dest, "file")
	s.Require().NoError(os.WriteFile(path, []byte("same\n"), 0o600))
	result := s.apply([]manifest.ConfigTarget{{Path: "file", Applications: []string{"app"}}})
	s.Equal("same\n", s.read("file"))
	s.True(hasEntryStatus(result, path, ApplyConflict))
	info, err := os.Stat(path)
	s.Require().NoError(err)
	s.Equal(os.FileMode(0o600), info.Mode().Perm())
	s.Require().NoError(os.Chmod(path, 0o644))
	adopted := s.apply([]manifest.ConfigTarget{{Path: "file", Applications: []string{"app"}}})
	s.True(hasEntryStatus(adopted, path, ApplySuccess))
}

func (s *applySuite) TestWholeSourceRejectedOutsideRequestedScope() {
	s.put("in_scope", "ok\n")
	s.put("run_once_outside", "echo forbidden\n")
	result := s.apply([]manifest.ConfigTarget{{Path: "in_scope", Applications: []string{"app"}}})
	s.NotEmpty(result.Diagnostics)
	s.Empty(result.Entries)
	s.False(fileExists(filepath.Join(s.dest, "in_scope")))
}

func (s *applySuite) TestNoTargetsAndGlobalTemplateFailureDoNotWrite() {
	s.put("safe", "safe\n")
	result := s.apply(nil)
	s.Empty(result.Entries)
	s.False(fileExists(filepath.Join(s.dest, "safe")))
	s.put("bad.tmpl", "{{ .Missing.Field }}")
	result = s.apply([]manifest.ConfigTarget{{Path: "safe", Applications: []string{"app"}}})
	s.NotEmpty(result.Diagnostics)
	s.False(fileExists(filepath.Join(s.dest, "safe")))
	blocked := s.apply([]manifest.ConfigTarget{{Path: "safe", Applications: []string{"blocked"}}}, "blocked")
	s.Empty(blocked.Diagnostics)
	s.Empty(blocked.Entries)
	s.Equal(ApplySkipped, targetStatus(blocked, filepath.Join(s.dest, "safe")))
	for _, target := range result.Targets {
		s.NotEqual(ApplySuccess, target.Status)
	}
}

func (s *applySuite) TestEffectiveDestinationOutsideRequestedScopeIsError() {
	s.put("dot_safe", "safe\n")
	outside := filepath.Join(s.root, "outside-effective-destination")
	result := s.apply([]manifest.ConfigTarget{{Path: outside, Applications: []string{"app"}}})
	s.Equal(ApplyError, targetStatus(result, outside))
	s.False(fileExists(outside))
}

func (s *applySuite) TestNormalSettingsSelectNondefaultDestinationAndSharedState() {
	destination := filepath.Join(s.root, "alternate-destination")
	state := filepath.Join(s.root, "normal-user-state.boltdb")
	configDir := filepath.Join(s.root, "xdg-config", "chezmoi")
	s.Require().NoError(os.MkdirAll(destination, 0o700))
	s.Require().NoError(os.MkdirAll(configDir, 0o700))
	config := "destDir = '" + destination + "'\npersistentState = '" + state + "'\n"
	s.Require().NoError(os.WriteFile(filepath.Join(configDir, "chezmoi.toml"), []byte(config), 0o600))
	s.put("tool", "configured\n")
	path := filepath.Join(destination, "tool")
	result := s.apply([]manifest.ConfigTarget{{Path: path, Applications: []string{"app"}}})
	s.True(hasEntryStatus(result, path, ApplySuccess))
	contents, err := os.ReadFile(path)
	s.Require().NoError(err)
	s.Equal("configured\n", string(contents))
	s.True(fileExists(state))
	s.False(fileExists(filepath.Join(s.dest, "tool")))
	xdgTarget := filepath.Join(s.root, "xdg-config", "tool")
	outside := s.apply([]manifest.ConfigTarget{{Path: xdgTarget, Applications: []string{"app"}}})
	s.Equal(ApplyError, targetStatus(outside, xdgTarget))
	s.False(fileExists(xdgTarget))
}

func (s *applySuite) TestNestedSourceRootSymlinksAndDirectoryDataArePreserved() {
	s.put(".chezmoiroot", "nested\n")
	s.put("nested/symlink_link", "target\n")
	s.put("nested/replacement", "file\n")
	s.put("nested/selected", "selected\n")
	s.put("nested/unselected", "unselected\n")
	s.Require().NoError(os.MkdirAll(filepath.Join(s.dest, "replacement"), 0o700))
	s.Require().NoError(os.WriteFile(filepath.Join(s.dest, "replacement/extra"), []byte("keep"), 0o600))
	result := s.apply([]manifest.ConfigTarget{
		{Path: "link", Applications: []string{"app"}},
		{Path: "replacement", Applications: []string{"app"}},
		{Path: "selected", Applications: []string{"app"}},
	})
	link, err := os.Readlink(filepath.Join(s.dest, "link"))
	s.Require().NoError(err)
	s.Equal("target", link)
	s.Equal("keep", s.read("replacement/extra"))
	s.Equal(ApplyConflict, targetStatus(result, filepath.Join(s.dest, "replacement")))
	s.Equal("selected\n", s.read("selected"))
	s.False(fileExists(filepath.Join(s.dest, "unselected")))
}

func hasEntryStatus(result ApplyResult, path string, status ApplyStatus) bool {
	entry, ok := findEntry(result, path)
	return ok && entry.Status == status
}

func findEntry(result ApplyResult, path string) (EntryResult, bool) {
	for _, entry := range result.Entries {
		if entry.Path == path {
			return entry, true
		}
	}
	return EntryResult{}, false
}

func targetStatus(result ApplyResult, path string) ApplyStatus {
	for _, target := range result.Targets {
		if target.Path == path {
			return target.Status
		}
	}
	return ""
}

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
