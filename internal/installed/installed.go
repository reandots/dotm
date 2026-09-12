// Package installed compares explicit native profile entries with desired applications.
package installed

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"dotm/internal/manifest"
)

type Report struct {
	Profile      string
	Applications []ApplicationStatus
	Entries      []Entry
	Extras       []Entry
}

type ApplicationStatus struct {
	ID       string
	State    string
	Packages []PackageStatus
}

type PackageStatus struct {
	Descriptor string
	State      string
	Entries    []string
}

type Entry struct {
	Name        string
	Active      bool
	AttrPath    string
	OriginalURL string
	URL         string
	Outputs     []string
	StorePaths  []string
	Descriptor  string
	Updateable  bool
}

func stateHome() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	if !filepath.IsAbs(base) {
		return "", fmt.Errorf("XDG_STATE_HOME must be absolute: %q", base)
	}
	return filepath.Clean(base), nil
}

func nixSystem() (string, error) {
	var arch string
	switch runtime.GOARCH {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	default:
		return "", fmt.Errorf("unsupported Nix architecture %q", runtime.GOARCH)
	}
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("unsupported Nix operating system %q", runtime.GOOS)
	}
	return arch + "-linux", nil
}

// profileExists distinguishes a genuinely missing directory from a dangling link,
// including a dangling ancestor of the profile path. It creates nothing.
func profileExists(profile string) (bool, error) {
	if _, err := os.Stat(profile); err == nil {
		return true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	for p := profile; ; p = filepath.Dir(p) {
		if _, err := os.Lstat(p); err == nil {
			_, err = os.Stat(p)
			return false, err
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		if p == filepath.Dir(p) {
			return false, nil
		}
	}
}

// Inspect reads only the standard user profile. Absence is an empty environment;
// an inaccessible, dangling, corrupt, or unsupported profile is an error.
func Inspect(ctx context.Context, desired manifest.Resolved) (Report, error) {
	base, err := stateHome()
	if err != nil {
		return Report{}, err
	}
	profile := filepath.Join(base, "nix", "profile")
	exists, err := profileExists(profile)
	if err != nil {
		return Report{}, fmt.Errorf("inspect profile %s: %w", profile, err)
	}
	if !exists {
		return classify(Report{Profile: profile}, desired), nil
	}
	// Nix 2.35.6 reports an existing non-profile file as an empty profile.
	// Require the native manifest before trusting that empty result.
	info, err := os.Stat(filepath.Join(profile, "manifest.json"))
	if err != nil {
		return Report{}, fmt.Errorf("inspect profile %s: native JSON manifest unavailable: %w", profile, err)
	}
	if !info.Mode().IsRegular() {
		return Report{}, fmt.Errorf("inspect profile %s: native JSON manifest is not a regular file", profile)
	}
	cmd := exec.CommandContext(ctx, "nix", "profile", "list", "--profile", profile, "--json")
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return Report{}, fmt.Errorf("nix profile list --profile %s --json: %w: %s", profile, err, strings.TrimSpace(string(exit.Stderr)))
		}
		return Report{}, fmt.Errorf("nix profile list --profile %s --json: %w", profile, err)
	}
	system, err := nixSystem()
	if err != nil {
		return Report{}, err
	}
	entries, err := parseEntries(out, system, filepath.Join(base, "dotm", "source"))
	if err != nil {
		return Report{}, err
	}
	return classify(Report{Profile: profile, Entries: entries}, desired), nil
}

func parseEntries(data []byte, system, source string) ([]Entry, error) {
	var root struct {
		Version  int                        `json:"version"`
		Elements map[string]json.RawMessage `json:"elements"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("decode Nix profile JSON: %w", err)
	}
	if root.Version != 3 || root.Elements == nil {
		return nil, fmt.Errorf("unsupported Nix profile JSON: expected version 3 and an elements object")
	}
	entries := make([]Entry, 0, len(root.Elements))
	for name, raw := range root.Elements {
		var native struct {
			Active      *bool           `json:"active"`
			AttrPath    string          `json:"attrPath"`
			OriginalURL string          `json:"originalUrl"`
			URL         string          `json:"url"`
			Outputs     json.RawMessage `json:"outputs"`
			StorePaths  []string        `json:"storePaths"`
		}
		if err := json.Unmarshal(raw, &native); err != nil || name == "" || native.Active == nil || len(native.StorePaths) == 0 {
			return nil, fmt.Errorf("unsupported Nix profile entry %q: invalid identity, active flag, or store paths", name)
		}
		hasSource := native.AttrPath != "" || native.OriginalURL != "" || native.URL != ""
		if hasSource && (native.AttrPath == "" || native.OriginalURL == "" || native.URL == "" || len(native.Outputs) == 0) {
			return nil, fmt.Errorf("unsupported Nix profile entry %q: incomplete source identity", name)
		}
		e := Entry{Name: name, Active: *native.Active, AttrPath: native.AttrPath, OriginalURL: native.OriginalURL, URL: native.URL, StorePaths: native.StorePaths, Updateable: hasSource}
		if len(native.Outputs) != 0 && string(native.Outputs) != "null" {
			if err := json.Unmarshal(native.Outputs, &e.Outputs); err != nil || len(e.Outputs) == 0 {
				return nil, fmt.Errorf("unsupported outputs in profile entry %q", name)
			}
			sort.Strings(e.Outputs)
			for i, output := range e.Outputs {
				if !outputName.MatchString(output) || (i > 0 && output == e.Outputs[i-1]) {
					return nil, fmt.Errorf("invalid outputs in profile entry %q", name)
				}
			}
		}
		for _, path := range e.StorePaths {
			if !filepath.IsAbs(path) {
				return nil, fmt.Errorf("invalid store path in profile entry %q", name)
			}
		}
		sort.Strings(e.StorePaths)
		e.Descriptor = identify(e, system, source)
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

// These match the catalog's deliberately restricted descriptor grammar, not
// arbitrary Nix expressions or quoted attribute segments.
var attrName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_'-]*(\.[a-zA-Z_][a-zA-Z0-9_'-]*)*$`)
var outputName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

func descriptor(key string) (manifest.Package, bool) {
	parts := strings.Split(key, "^")
	p := manifest.Package{Attr: parts[0]}
	if len(parts) > 2 || !attrName.MatchString(p.Attr) {
		return manifest.Package{}, false
	}
	if len(parts) == 2 {
		p.Outputs = strings.Split(parts[1], ",")
		for i, output := range p.Outputs {
			if !outputName.MatchString(output) || (i > 0 && output <= p.Outputs[i-1]) {
				return manifest.Package{}, false
			}
		}
	}
	return p, true
}

func isNixpkgs(ref string) bool {
	base, query, _ := strings.Cut(ref, "?")
	const repo = "github:NixOS/nixpkgs"
	if base != repo && !strings.HasPrefix(base, repo+"/") {
		return false
	}
	params, err := url.ParseQuery(query)
	return err == nil && params.Get("dir") == ""
}

func identify(e Entry, system, source string) string {
	prefix := "packages." + system + ".dotm_"
	if strings.HasPrefix(e.AttrPath, prefix) && e.OriginalURL == "path:"+source {
		// Canonical descriptor bytes live in the native manifest's attrPath.
		// Catalog removal cannot erase this recipe; no mutable sidecar is needed.
		encoded := strings.TrimPrefix(e.AttrPath, prefix)
		decoded, err := hex.DecodeString(encoded)
		if err != nil || hex.EncodeToString(decoded) != encoded {
			return ""
		}
		p, ok := descriptor(string(decoded))
		if !ok || !sameOutputs(p.Outputs, e.Outputs) {
			return ""
		}
		return p.Key()
	}
	prefix = "legacyPackages." + system + "."
	if strings.HasPrefix(e.AttrPath, prefix) && isNixpkgs(e.OriginalURL) && isNixpkgs(e.URL) {
		p := manifest.Package{Attr: strings.TrimPrefix(e.AttrPath, prefix), Outputs: e.Outputs}
		if !attrName.MatchString(p.Attr) {
			return ""
		}
		return p.Key()
	}
	return ""
}

func sameOutputs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func classify(r Report, desired manifest.Resolved) Report {
	sort.Slice(r.Entries, func(i, j int) bool { return r.Entries[i].Name < r.Entries[j].Name })
	byDescriptor := make(map[string][]int)
	for i := range r.Entries {
		e := &r.Entries[i]
		if e.Descriptor != "" {
			byDescriptor[e.Descriptor] = append(byDescriptor[e.Descriptor], i)
		}
	}
	wanted := make(map[string]bool)
	for _, app := range desired.Applications {
		as := ApplicationStatus{ID: app.ID, State: "installed"}
		complete, evidence := 0, false
		for _, p := range app.Packages {
			key := p.Key()
			wanted[key] = true
			ps := PackageStatus{Descriptor: key, State: "missing"}
			active := 0
			for _, i := range byDescriptor[key] {
				e := &r.Entries[i]
				ps.Entries = append(ps.Entries, e.Name)
				if e.Active {
					active++
				}
			}
			switch {
			case active > 1:
				ps.State = "ambiguous"
				evidence = true
			case active == 1:
				ps.State = "installed"
				complete++
				evidence = true
			}
			as.Packages = append(as.Packages, ps)
		}
		if complete != len(app.Packages) {
			as.State = "missing"
			if evidence {
				as.State = "partial"
			}
		}
		r.Applications = append(r.Applications, as)
	}
	for _, e := range r.Entries {
		if !e.Active || e.Descriptor == "" || !wanted[e.Descriptor] {
			r.Extras = append(r.Extras, e)
		}
	}
	return r
}
