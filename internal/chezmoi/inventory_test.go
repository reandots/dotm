package chezmoi

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"dotm/internal/manifest"

	"github.com/stretchr/testify/suite"
)

type inventorySuite struct {
	suite.Suite
}

func TestInventorySuite(t *testing.T) {
	suite.Run(t, new(inventorySuite))
}

func (s *inventorySuite) TestAncestorSymlinkIsNotTraversed() {
	root := s.T().TempDir()
	referent := s.T().TempDir()
	s.Require().NoError(os.WriteFile(filepath.Join(referent, "secret"), []byte("x"), 0o600))
	link := filepath.Join(root, "link")
	s.Require().NoError(os.Symlink(referent, link))
	_, err := lstatNoSymlinkAncestors(root, filepath.Join(link, "secret"))
	s.ErrorIs(err, fs.ErrNotExist)
}

func (s *inventorySuite) TestRootsDeduplicateOverlappingScopesAndRejectRelativeXDG() {
	home := s.T().TempDir()
	s.T().Setenv("HOME", home)
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		s.T().Setenv(key, filepath.Join(home, key))
	}
	roots, err := inventoryRoots()
	s.Require().NoError(err)
	s.Equal([]string{home}, roots)
	s.T().Setenv("XDG_CONFIG_HOME", "relative")
	_, err = inventoryRoots()
	s.Error(err)
}

func (s *policySuite) TestInventorySeparatesAcknowledgementsFromProfileFiltering() {
	// Keep every supported scan root under the isolated destination.
	s.config = filepath.Join(s.home, ".config", "chezmoi", "chezmoi.toml")
	for key, value := range map[string]string{
		"XDG_CONFIG_HOME": filepath.Join(s.home, ".config"),
		"XDG_DATA_HOME":   filepath.Join(s.home, "data"),
		"XDG_STATE_HOME":  filepath.Join(s.home, "state"),
		"XDG_CACHE_HOME":  filepath.Join(s.home, "cache"),
	} {
		s.T().Setenv(key, value)
	}
	s.configure()
	s.integrate()
	s.write(filepath.Join(s.source, ".chezmoiignore"),
		mustReadInventoryFixture(s)+"\nroot-native\nboundary/ignored\n")
	s.write(filepath.Join(s.source, "dot_config", "selected", ".chezmoiignore"), "*.db\n")
	for _, name := range []string{"same", "both", "missing", "line\n M .config/selected/same"} {
		s.write(filepath.Join(s.source, "dot_config", "selected", "literal_"+name), "initial\n")
	}
	s.write(filepath.Join(s.source, "dot_config", "other", "settings"), "other\n")
	_, err := policyCommand(context.Background(), applyCommand{source: s.source}, "apply")
	s.Require().NoError(err)
	s.write(filepath.Join(s.home, ".config", "selected", "both"), "local\n")
	s.write(filepath.Join(s.source, "dot_config", "selected", "literal_both"), "desired\n")
	s.write(filepath.Join(s.home, ".config", "selected", "line\n M .config/selected/same"), "local\n")
	s.Require().NoError(os.Remove(filepath.Join(s.home, ".config", "selected", "missing")))
	for _, path := range []string{
		"boundary/known/data", "boundary/unknown/data", "boundary/ignored/data",
		".config/selected/new", ".config/selected/cache.db", "selected-cache/data", "root-native",
	} {
		s.write(filepath.Join(s.home, path), "data\n")
	}
	declarations := []manifest.FileDeclaration{
		{Field: "configs", Application: "selected", Selected: true, Path: filepath.Join(s.home, ".config", "selected")},
		{Field: "configs", Application: "other", Path: filepath.Join(s.home, ".config", "other")},
		{Field: "unmanaged", Application: "selected", Selected: true, Path: filepath.Join(s.home, "selected-cache")},
		{Field: "unmanaged", Path: filepath.Join(s.home, "boundary", "known")},
	}
	policy := s.publish(s.input("work", declarations...))
	result, err := Inventory(context.Background(), s.source, policy)
	s.Require().NoError(err)
	entries := make(map[string]InventoryEntry)
	for _, entry := range result.Entries {
		path, relErr := filepath.Rel(s.home, entry.Path)
		s.Require().NoError(relErr)
		entries[path] = entry
	}
	s.Contains(entries, "boundary/unknown")
	s.NotContains(entries, "boundary")
	s.NotContains(entries, "boundary/known")
	s.NotContains(entries, "boundary/ignored")
	s.Contains(entries, ".config/other/settings")
	s.False(entries[".config/other/settings"].Managed)
	s.Contains(entries, ".config/selected/new")
	s.NotContains(entries, ".config/selected/same")
	s.NotContains(entries, ".config/selected/cache.db")
	s.NotContains(entries, "selected-cache")
	s.NotContains(entries, "root-native")
	both := entries[".config/selected/both"]
	s.True(both.Managed)
	s.NotEqual(byte(' '), both.Local)
	s.NotEqual(byte(' '), both.Pending)
	s.Equal([]string{"selected"}, both.Applications)
	s.Contains(entries, ".config/selected/missing")
	s.Contains(entries, ".config/selected/line\n M .config/selected/same")
}

func mustReadInventoryFixture(s *policySuite) string {
	s.T().Helper()
	content, err := os.ReadFile(filepath.Join(s.source, ".chezmoiignore"))
	s.Require().NoError(err)
	return string(content)
}
