package installed

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"dotm/internal/manifest"

	"github.com/stretchr/testify/suite"
)

type InstalledSuite struct {
	suite.Suite
}

func TestInstalledSuite(t *testing.T) {
	suite.Run(t, new(InstalledSuite))
}

func (s *InstalledSuite) nativeJSON(entries map[string]any) []byte {
	s.T().Helper()
	data, err := json.Marshal(map[string]any{"version": 3, "elements": entries})
	s.Require().NoError(err)
	return data
}

func sourceEntry(attr, source string, outputs any) map[string]any {
	return map[string]any{"active": true, "attrPath": attr, "originalUrl": source, "url": source + "?narHash=locked", "outputs": outputs, "storePaths": []string{"/nix/store/example-tool"}}
}

func (s *InstalledSuite) TestNativeDescriptorIdentity() {
	const source = "/state/dotm/source"
	const system = "x86_64-linux"
	managed := func(key string) string { return "packages." + system + ".dotm_" + hex.EncodeToString([]byte(key)) }
	for _, tt := range []struct {
		name, attr, original, want string
		outputs                    any
	}{
		{"managed default", managed("hello"), "path:" + source, "hello", nil},
		{"managed explicit", managed("jq^bin,man"), "path:" + source, "jq^bin,man", []string{"man", "bin"}},
		{"encoded selector disagrees", managed("jq^bin,man"), "path:" + source, "", nil},
		{"encoded default disagrees", managed("jq"), "path:" + source, "", []string{"out"}},
		{"different local source", managed("hello"), "path:/other", "", nil},
		{"foreign system", "packages.aarch64-linux.dotm_68656c6c6f", "path:" + source, "", nil},
		{"invalid encoded descriptor", managed("a;bad"), "path:" + source, "", nil},
		{"noncanonical encoded outputs", managed("jq^man,bin"), "path:" + source, "", []string{"bin", "man"}},
		{"legacy default", "legacyPackages." + system + ".hello", "github:NixOS/nixpkgs/old-revision", "hello", nil},
		{"legacy explicit out", "legacyPackages." + system + ".hello", "github:NixOS/nixpkgs/new-revision", "hello^out", []string{"out"}},
		{"legacy unsorted outputs", "legacyPackages." + system + ".jq", "github:NixOS/nixpkgs/revision", "jq^bin,man", []string{"man", "bin"}},
		{"different repository", "legacyPackages." + system + ".hello", "github:someone/nixpkgs/revision", "", nil},
		{"unknown registry", "legacyPackages." + system + ".hello", "flake:nixpkgs", "", nil},
		{"different output tree", "packages." + system + ".hello", "github:NixOS/nixpkgs/revision", "", nil},
	} {
		s.Run(tt.name, func() {
			entries, err := parseEntries(s.nativeJSON(map[string]any{"native": sourceEntry(tt.attr, tt.original, tt.outputs)}), system, source)
			s.Require().NoError(err)
			s.Equal(tt.want, entries[0].Descriptor)
		})
	}
}

func (s *InstalledSuite) TestUnknownEntriesAndRetainedDescriptors() {
	entries, err := parseEntries(s.nativeJSON(map[string]any{
		"store-only": map[string]any{"active": true, "storePaths": []string{"/nix/store/unknown"}},
		"foreign":    sourceEntry("packages.x86_64-linux.tool", "github:someone/tool/rev", nil),
		"retained":   sourceEntry("packages.x86_64-linux.dotm_68656c6c6f", "path:/state/dotm/source", nil),
	}), "x86_64-linux", "/state/dotm/source")
	s.Require().NoError(err)
	r := classify(Report{Entries: entries}, manifest.Resolved{})
	var names []string
	for _, e := range r.Extras {
		names = append(names, e.Name)
	}
	s.Equal([]string{"foreign", "retained", "store-only"}, names, "extras retain all native entries")
	foreign := r.Entries[0]
	s.Empty(foreign.Descriptor)
	s.True(foreign.Updateable)
	s.Equal("packages.x86_64-linux.tool", foreign.AttrPath)
	s.Equal("github:someone/tool/rev", foreign.OriginalURL)
	s.Equal("hello", r.Entries[1].Descriptor, "catalog removal retains descriptor identity")
	storeOnly := r.Entries[2]
	s.Empty(storeOnly.Descriptor)
	s.False(storeOnly.Updateable, "store-only entries have no update recipe")
	s.Equal([]string{"/nix/store/unknown"}, storeOnly.StorePaths)
}

func (s *InstalledSuite) TestRejectMalformedNativeState() {
	for _, input := range []string{"", "null", "{}", `{"version":4,"elements":{}}`, `{"version":3,"elements":null}`, `{"version":3,"elements":[]}`} {
		_, err := parseEntries([]byte(input), "x86_64-linux", "/source")
		s.Error(err, "unsupported state: %s", input)
	}
	for _, entry := range []any{
		nil,
		map[string]any{},
		map[string]any{"active": true, "storePaths": []string{}},
		map[string]any{"active": "true", "storePaths": []string{"/nix/store/x"}},
		map[string]any{"active": true, "storePaths": []string{"relative"}},
		map[string]any{"active": true, "storePaths": []string{"/nix/store/x"}, "attrPath": "tool"},
		sourceEntry("tool", "path:/source", []string{}),
		sourceEntry("tool", "path:/source", []string{"out", "out"}),
		sourceEntry("tool", "path:/source", []string{"bad^output"}),
		sourceEntry("tool", "path:/source", "out"),
	} {
		_, err := parseEntries(s.nativeJSON(map[string]any{"bad": entry}), "x86_64-linux", "/source")
		s.Error(err, "malformed entry: %#v", entry)
	}
}

func (s *InstalledSuite) TestClassifySharedPartialMissingAndExtras() {
	r := classify(Report{Entries: []Entry{
		{Name: "unknown", Active: true},
		{Name: "shared", Active: true, Descriptor: "hello"},
		{Name: "which", Active: true, Descriptor: "which"},
		{Name: "inactive", Active: false, Descriptor: "off"},
		{Name: "wrong-outputs", Active: true, Descriptor: "jq^out"},
	}}, manifest.Resolved{Applications: []manifest.Application{
		{ID: "complete", Packages: []manifest.Package{{Attr: "hello"}, {Attr: "which"}}},
		{ID: "partial", Packages: []manifest.Package{{Attr: "hello"}, {Attr: "off"}}},
		{ID: "missing", Packages: []manifest.Package{{Attr: "jq"}}},
		{ID: "companion", Packages: []manifest.Package{{Attr: "hello"}}},
	}})
	var states []string
	for _, app := range r.Applications {
		states = append(states, app.State)
	}
	s.Equal([]string{"installed", "partial", "missing", "installed"}, states)
	inactive := r.Applications[1].Packages[1]
	s.Equal("missing", inactive.State, "inactive entry cannot satisfy a desired package")
	s.Equal([]string{"inactive"}, inactive.Entries)
	var extras []string
	for _, e := range r.Extras {
		extras = append(extras, e.Name)
	}
	s.Equal([]string{"inactive", "unknown", "wrong-outputs"}, extras, "shared entries are not extras")
}

func (s *InstalledSuite) TestDuplicateActiveEntriesAreAmbiguous() {
	for _, tt := range []struct {
		name                   string
		secondActive           bool
		appState, packageState string
	}{
		{"two active entries", true, "partial", "ambiguous"},
		{"inactive duplicate", false, "installed", "installed"},
	} {
		s.Run(tt.name, func() {
			r := classify(Report{Entries: []Entry{
				{Name: "one", Active: true, Descriptor: "tool"},
				{Name: "two", Active: tt.secondActive, Descriptor: "tool"},
			}}, manifest.Resolved{Applications: []manifest.Application{{ID: "app", Packages: []manifest.Package{{Attr: "tool"}}}}})
			s.Equal(tt.appState, r.Applications[0].State)
			s.Equal(tt.packageState, r.Applications[0].Packages[0].State)
			s.Equal([]string{"one", "two"}, r.Applications[0].Packages[0].Entries, "duplicate identities remain visible")
		})
	}
}

func (s *InstalledSuite) TestMissingAndDanglingProfiles() {
	base := filepath.Join(s.T().TempDir(), "state")
	s.T().Setenv("XDG_STATE_HOME", base)
	// A missing environment does not need Nix and must remain absent.
	s.T().Setenv("PATH", "")
	r, err := Inspect(context.Background(), manifest.Resolved{Applications: []manifest.Application{{ID: "app", Packages: []manifest.Package{{Attr: "tool"}}}}})
	s.Require().NoError(err)
	s.Equal("missing", r.Applications[0].State)
	_, err = os.Lstat(base)
	s.ErrorIs(err, os.ErrNotExist, "inspection must not create state")
	s.Require().NoError(os.MkdirAll(filepath.Join(base, "nix"), 0700))
	profile := filepath.Join(base, "nix", "profile")
	s.Require().NoError(os.Symlink("missing-generation", profile))
	_, err = Inspect(context.Background(), manifest.Resolved{})
	s.Error(err, "dangling profile must not be reported as empty")
	parent := filepath.Join(s.T().TempDir(), "linked-state")
	s.Require().NoError(os.Symlink("missing-state", parent))
	s.T().Setenv("XDG_STATE_HOME", parent)
	_, err = Inspect(context.Background(), manifest.Resolved{})
	s.Error(err, "dangling state directory must not be reported as empty")
	s.T().Setenv("XDG_STATE_HOME", "relative")
	_, err = Inspect(context.Background(), manifest.Resolved{})
	s.Error(err, "relative state path must be rejected")
}

func (s *InstalledSuite) TestRejectNonProfileLocations() {
	for _, tt := range []struct {
		name      string
		directory bool
		want      error
	}{
		{"regular file", false, syscall.ENOTDIR},
		{"directory without manifest", true, os.ErrNotExist},
	} {
		s.Run(tt.name, func() {
			base := s.T().TempDir()
			s.T().Setenv("XDG_STATE_HOME", base)
			s.T().Setenv("PATH", "")
			profile := filepath.Join(base, "nix", "profile")
			s.Require().NoError(os.MkdirAll(filepath.Dir(profile), 0700))
			if tt.directory {
				s.Require().NoError(os.Mkdir(profile, 0700))
			} else {
				s.Require().NoError(os.WriteFile(profile, []byte("not a profile"), 0600))
			}
			_, err := Inspect(context.Background(), manifest.Resolved{})
			s.ErrorIs(err, tt.want)
		})
	}
}
