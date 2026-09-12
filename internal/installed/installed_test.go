package installed

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"

	"dotm/internal/manifest"
)

func nativeJSON(t *testing.T, entries map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"version": 3, "elements": entries})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func sourceEntry(attr, source string, outputs any) map[string]any {
	return map[string]any{"active": true, "attrPath": attr, "originalUrl": source, "url": source + "?narHash=locked", "outputs": outputs, "storePaths": []string{"/nix/store/example-tool"}}
}

func TestNativeDescriptorIdentity(t *testing.T) {
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
		t.Run(tt.name, func(t *testing.T) {
			entries, err := parseEntries(nativeJSON(t, map[string]any{"native": sourceEntry(tt.attr, tt.original, tt.outputs)}), system, source)
			if err != nil {
				t.Fatal(err)
			}
			if got := entries[0].Descriptor; got != tt.want {
				t.Fatalf("descriptor: got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUnknownEntriesAndRetainedDescriptors(t *testing.T) {
	entries, err := parseEntries(nativeJSON(t, map[string]any{
		"store-only": map[string]any{"active": true, "storePaths": []string{"/nix/store/unknown"}},
		"foreign":    sourceEntry("packages.x86_64-linux.tool", "github:someone/tool/rev", nil),
		"retained":   sourceEntry("packages.x86_64-linux.dotm_68656c6c6f", "path:/state/dotm/source", nil),
	}), "x86_64-linux", "/state/dotm/source")
	if err != nil {
		t.Fatal(err)
	}
	r := classify(Report{Entries: entries}, manifest.Resolved{})
	var names []string
	for _, e := range r.Extras {
		names = append(names, e.Name)
	}
	if !reflect.DeepEqual(names, []string{"foreign", "retained", "store-only"}) {
		t.Fatalf("extras lost native entries: %v", names)
	}
	if e := r.Entries[0]; e.Descriptor != "" || !e.Updateable || e.AttrPath != "packages.x86_64-linux.tool" || e.OriginalURL != "github:someone/tool/rev" {
		t.Fatalf("foreign identity lost: %#v", e)
	}
	if e := r.Entries[1]; e.Descriptor != "hello" {
		t.Fatalf("catalog removal lost retained descriptor: %#v", e)
	}
	if e := r.Entries[2]; e.Descriptor != "" || e.Updateable || !reflect.DeepEqual(e.StorePaths, []string{"/nix/store/unknown"}) {
		t.Fatalf("store-only recipe guessed: %#v", e)
	}
}

func TestRejectMalformedNativeState(t *testing.T) {
	for _, input := range []string{"", "null", "{}", `{"version":4,"elements":{}}`, `{"version":3,"elements":null}`, `{"version":3,"elements":[]}`} {
		if _, err := parseEntries([]byte(input), "x86_64-linux", "/source"); err == nil {
			t.Fatalf("accepted unsupported state: %s", input)
		}
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
		if _, err := parseEntries(nativeJSON(t, map[string]any{"bad": entry}), "x86_64-linux", "/source"); err == nil {
			t.Fatalf("accepted malformed entry: %#v", entry)
		}
	}
}

func TestClassifySharedPartialMissingAndExtras(t *testing.T) {
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
	if !reflect.DeepEqual(states, []string{"installed", "partial", "missing", "installed"}) {
		t.Fatalf("application states: %v", states)
	}
	if p := r.Applications[1].Packages[1]; p.State != "missing" || !reflect.DeepEqual(p.Entries, []string{"inactive"}) {
		t.Fatalf("inactive package satisfied desired: %#v", p)
	}
	var extras []string
	for _, e := range r.Extras {
		extras = append(extras, e.Name)
	}
	if !reflect.DeepEqual(extras, []string{"inactive", "unknown", "wrong-outputs"}) {
		t.Fatalf("shared entry removed or extra omitted: %v", extras)
	}
}

func TestDuplicateActiveEntriesAreAmbiguous(t *testing.T) {
	for _, tt := range []struct {
		secondActive           bool
		appState, packageState string
	}{
		{true, "partial", "ambiguous"},
		{false, "installed", "installed"},
	} {
		r := classify(Report{Entries: []Entry{
			{Name: "one", Active: true, Descriptor: "tool"},
			{Name: "two", Active: tt.secondActive, Descriptor: "tool"},
		}}, manifest.Resolved{Applications: []manifest.Application{{ID: "app", Packages: []manifest.Package{{Attr: "tool"}}}}})
		if r.Applications[0].State != tt.appState || r.Applications[0].Packages[0].State != tt.packageState {
			t.Fatalf("duplicate active=%v: %#v", tt.secondActive, r.Applications)
		}
		if !reflect.DeepEqual(r.Applications[0].Packages[0].Entries, []string{"one", "two"}) {
			t.Fatal("ambiguous identities were hidden")
		}
	}
}

func TestMissingAndDanglingProfiles(t *testing.T) {
	base := filepath.Join(t.TempDir(), "state")
	t.Setenv("XDG_STATE_HOME", base)
	// A missing environment does not need Nix and must remain absent.
	t.Setenv("PATH", "")
	r, err := Inspect(context.Background(), manifest.Resolved{Applications: []manifest.Application{{ID: "app", Packages: []manifest.Package{{Attr: "tool"}}}}})
	if err != nil || r.Applications[0].State != "missing" {
		t.Fatalf("missing profile: %#v, %v", r, err)
	}
	if _, err := os.Lstat(base); !os.IsNotExist(err) {
		t.Fatalf("inspection created state: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(base, "nix"), 0700); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(base, "nix", "profile")
	if err := os.Symlink("missing-generation", profile); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(context.Background(), manifest.Resolved{}); err == nil {
		t.Fatal("dangling profile reported as empty")
	}
	parent := filepath.Join(t.TempDir(), "linked-state")
	if err := os.Symlink("missing-state", parent); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", parent)
	if _, err := Inspect(context.Background(), manifest.Resolved{}); err == nil {
		t.Fatal("dangling state directory reported as empty")
	}
	t.Setenv("XDG_STATE_HOME", "relative")
	if _, err := Inspect(context.Background(), manifest.Resolved{}); err == nil {
		t.Fatal("relative state path accepted")
	}
}

func TestRejectNonProfileLocations(t *testing.T) {
	for _, directory := range []bool{false, true} {
		base := t.TempDir()
		t.Setenv("XDG_STATE_HOME", base)
		t.Setenv("PATH", "")
		profile := filepath.Join(base, "nix", "profile")
		if err := os.MkdirAll(filepath.Dir(profile), 0700); err != nil {
			t.Fatal(err)
		}
		want := error(syscall.ENOTDIR)
		if directory {
			if err := os.Mkdir(profile, 0700); err != nil {
				t.Fatal(err)
			}
			want = os.ErrNotExist
		} else if err := os.WriteFile(profile, []byte("not a profile"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Inspect(context.Background(), manifest.Resolved{}); !errors.Is(err, want) {
			t.Fatalf("invalid profile directory=%v: got %v, want filesystem error %v", directory, err, want)
		}
	}
}
