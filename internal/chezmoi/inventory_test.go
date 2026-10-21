package chezmoi

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
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
	s.write(filepath.Join(s.source, "dot_config", "other", "app", "aliases"), "alias\n")
	s.write(filepath.Join(s.source, "dot_config", "mixed", "settings"), "mixed\n")
	_, err := policyCommand(context.Background(), applyCommand{source: s.source}, "apply")
	s.Require().NoError(err)
	s.write(filepath.Join(s.home, ".config", "selected", "both"), "local\n")
	s.write(filepath.Join(s.source, "dot_config", "selected", "literal_both"), "desired\n")
	s.write(filepath.Join(s.home, ".config", "selected", "line\n M .config/selected/same"), "local\n")
	s.Require().NoError(os.Remove(filepath.Join(s.home, ".config", "selected", "missing")))
	for _, path := range []string{
		"boundary/known/data", "boundary/unknown/data", "boundary/ignored/data",
		".config/selected/new", ".config/selected/cache.db", "selected-cache/data", "root-native",
		".config/mixed/hidden/data",
	} {
		s.write(filepath.Join(s.home, path), "data\n")
	}
	s.write(filepath.Join(s.source, "dot_config", "mixed", ".chezmoiignore"), "hidden/\n")
	s.Require().NoError(os.Symlink("settings", filepath.Join(s.home, ".config", "other", "link")))
	declarations := []manifest.FileDeclaration{
		{Field: "configs", Application: "selected", Selected: true, Path: filepath.Join(s.home, ".config", "selected")},
		{Field: "configs", Application: "other", Path: filepath.Join(s.home, ".config", "other")},
		{Field: "configs", Application: "mixed", Path: filepath.Join(s.home, ".config", "mixed")},
		{Field: "unmanaged", Application: "selected", Selected: true, Path: filepath.Join(s.home, "selected-cache")},
		{Field: "unmanaged", Path: filepath.Join(s.home, "boundary", "known")},
	}
	policy := s.publish(s.input("work", declarations...))
	result, err := Inventory(context.Background(), s.source, policy)
	s.Require().NoError(err)
	entries := make(map[string]InventoryEntry)
	s.True(sort.SliceIsSorted(result.Entries, func(i, j int) bool {
		return result.Entries[i].Path < result.Entries[j].Path
	}))
	for _, entry := range result.Entries {
		path, relErr := filepath.Rel(s.home, entry.Path)
		s.Require().NoError(relErr)
		entries[path] = entry
	}
	s.Contains(entries, "boundary/unknown")
	s.NotContains(entries, "boundary")
	s.NotContains(entries, "boundary/known")
	s.NotContains(entries, "boundary/ignored")
	s.Contains(entries, ".config/other")
	s.False(entries[".config/other"].Managed)
	s.Equal("directory", entries[".config/other"].Type)
	s.NotContains(entries, ".config/other/settings")
	s.NotContains(entries, ".config/other/app/aliases")
	s.NotContains(entries, ".config/other/link")
	s.Contains(entries, ".config/mixed")
	s.NotContains(entries, ".config/mixed/settings")
	s.NotContains(entries, ".config/mixed/hidden")
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

func (s *policySuite) TestInventoryCollapsesNonselectedNestedConfigDeclarations() {
	s.config = filepath.Join(s.home, ".config", "chezmoi", "chezmoi.toml")
	for key, value := range map[string]string{
		"XDG_CONFIG_HOME": filepath.Join(s.home, ".config"),
		"XDG_DATA_HOME":   filepath.Join(s.home, "data"),
		"XDG_STATE_HOME":  filepath.Join(s.home, "state"),
		"XDG_CACHE_HOME":  filepath.Join(s.home, "cache"),
	} {
		s.T().Setenv(key, value)
	}
	s.integrate()
	plugin := filepath.Join("plugins", "config", "cloudmanic.herdr-plus")
	s.write(filepath.Join(s.source, "dot_config", "herdr", "config.toml"), "setting = true\n")
	s.write(filepath.Join(s.source, "dot_config", "herdr", plugin, "settings.toml"), "setting = true\n")
	s.write(filepath.Join(s.source, "dot_config", "herdr", ".chezmoiignore"),
		"*.log\n.plugins.lock\nplugins.json\nsessions\nsession.json\n"+
			"session-snapshots\nsession-snapshots/**\nplugins/github\nplugins/github/**\n")
	_, err := policyCommand(context.Background(), applyCommand{source: s.source}, "apply")
	s.Require().NoError(err)
	herdr := filepath.Join(s.home, ".config", "herdr")
	s.write(filepath.Join(herdr, "cache", "data"), "data\n")
	for _, path := range []string{
		"herdr-server.log", ".plugins.lock", "plugins.json", "session.json",
		"sessions/example/session.json", "session-snapshots/snapshot.json", "plugins/github/example/data",
	} {
		s.write(filepath.Join(herdr, path), "runtime\n")
	}
	for _, selected := range []bool{false, true} {
		name := "nonselected"
		if selected {
			name = "selected"
		}
		s.Run(name, func() {
			policy := s.publish(s.input("work",
				manifest.FileDeclaration{
					Field: "configs", Application: "herdr", Selected: selected, Path: filepath.Join(herdr, "config.toml"),
				},
				manifest.FileDeclaration{
					Field: "configs", Application: "herdr", Selected: selected, Path: filepath.Join(herdr, plugin),
				},
			))
			result, inventoryErr := Inventory(context.Background(), s.source, policy)
			s.Require().NoError(inventoryErr)
			var entries []InventoryEntry
			for _, entry := range result.Entries {
				if within(herdr, entry.Path) {
					entries = append(entries, entry)
				}
			}
			path := herdr
			if selected {
				path = filepath.Join(herdr, "cache")
			}
			s.Equal([]InventoryEntry{{Path: path, Type: "directory"}}, entries)
			if selected {
				return
			}
			acknowledgement := filepath.Join(herdr, "cache")
			policy.Input.Files.Declarations = append(policy.Input.Files.Declarations, manifest.FileDeclaration{
				Field: "unmanaged", Path: acknowledgement,
			})
			policy = s.publish(policy.Input)
			result, inventoryErr = Inventory(context.Background(), s.source, policy)
			s.Require().NoError(inventoryErr)
			paths := make(map[string]bool)
			for _, entry := range result.Entries {
				paths[entry.Path] = true
			}
			s.NotContains(paths, herdr)
			s.NotContains(paths, acknowledgement)
			s.Contains(paths, filepath.Join(herdr, "config.toml"))
			s.Contains(paths, filepath.Join(herdr, "plugins"))
		})
	}
}

func mustReadInventoryFixture(s *policySuite) string {
	s.T().Helper()
	content, err := os.ReadFile(filepath.Join(s.source, ".chezmoiignore"))
	s.Require().NoError(err)
	return string(content)
}
