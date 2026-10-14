package installed

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"dotm/internal/manifest"
)

const sourcePointerName = "source-current"

var sourceID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Path identifies one source instance, never a mutable alias shared by instances.
// Unhealthy state is recoverable; filesystem access errors are not corruption.
type sourceState struct {
	Path    string
	Healthy bool
	Reason  string
}

func validSourcePath(base, path string) bool {
	return path == filepath.Join(base, "dotm", "source") ||
		(filepath.Dir(path) == filepath.Join(base, "dotm", "sources") && sourceID.MatchString(filepath.Base(path)))
}

func readSourceState(base string) (sourceState, error) {
	root := filepath.Join(base, "dotm")
	pointer, reason, err := readSourceFile(filepath.Join(root, sourcePointerName))
	if err != nil {
		return sourceState{}, err
	}
	var path string
	if reason != "" {
		if reason != "missing" {
			return sourceState{Reason: "current source pointer is " + reason}, nil
		}
		// Existing installations without a pointer can keep their healthy legacy
		// source. Once rotation has occurred, pointer loss must not adopt it again.
		if _, statErr := os.Lstat(filepath.Join(root, "sources")); statErr == nil {
			return sourceState{Reason: "current source pointer is missing"}, nil
		} else if !os.IsNotExist(statErr) {
			return sourceState{}, fmt.Errorf("inspect source instances: %w", statErr)
		}
		path = filepath.Join(root, "source")
	} else {
		id := strings.TrimSuffix(string(pointer), "\n")
		if !sourceID.MatchString(id) {
			return sourceState{Reason: "current source pointer is corrupt"}, nil
		}
		path = filepath.Join(root, "sources", id)
	}
	return inspectSource(path)
}

func readSourceFile(path string) ([]byte, string, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, "missing", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("inspect source state %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, "not a regular file", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read source state %q: %w", path, err)
	}
	return data, "", nil
}

func inspectSource(path string) (sourceState, error) {
	state := sourceState{Path: path}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		state.Reason = "source directory is missing"
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("inspect source directory: %w", err)
	}
	if !info.IsDir() {
		state.Reason = "source path is not a directory"
		return state, nil
	}
	flake, reason, err := readSourceFile(filepath.Join(path, "flake.nix"))
	if err != nil {
		return state, err
	}
	if reason != "" {
		state.Reason = "source flake is " + reason
		return state, nil
	}
	system, err := nixSystem()
	if err != nil {
		return state, err
	}
	if !validGeneratedSource(string(flake), system) {
		state.Reason = "generated source flake is corrupt"
		return state, nil
	}
	lock, reason, err := readSourceFile(filepath.Join(path, "flake.lock"))
	if err != nil {
		return state, err
	}
	if reason != "" {
		state.Reason = "source lock is " + reason
		return state, nil
	}
	if !validSourceLock(lock) {
		state.Reason = "source lock is corrupt or unsupported"
		return state, nil
	}
	state.Healthy = true
	return state, nil
}

// Generated flakes contain only canonical descriptor-derived expressions. Rebuild
// that exact format rather than evaluating an arbitrary damaged Nix expression.
func validGeneratedSource(content, system string) bool {
	var packages []manifest.Package
	seen := make(map[string]bool)
	for _, line := range strings.Split(content, "\n") {
		if !strings.HasPrefix(line, "      dotm_") {
			continue
		}
		encoded, _, ok := strings.Cut(strings.TrimPrefix(line, "      dotm_"), " = ")
		if !ok {
			return false
		}
		decoded, err := hex.DecodeString(encoded)
		if err != nil {
			return false
		}
		p, ok := descriptor(string(decoded))
		if !ok || seen[p.Key()] {
			return false
		}
		seen[p.Key()] = true
		packages = append(packages, p)
	}
	return renderSource(system, packages) == content
}

func validSourceLock(data []byte) bool {
	var lock struct {
		Version int    `json:"version"`
		Root    string `json:"root"`
		Nodes   map[string]struct {
			Inputs map[string]string `json:"inputs"`
			Locked struct {
				Type    string `json:"type"`
				Owner   string `json:"owner"`
				Repo    string `json:"repo"`
				Rev     string `json:"rev"`
				NarHash string `json:"narHash"`
			} `json:"locked"`
		} `json:"nodes"`
	}
	if json.Unmarshal(data, &lock) != nil || lock.Version != 7 || lock.Root == "" {
		return false
	}
	root, ok := lock.Nodes[lock.Root]
	if !ok || root.Inputs["nixpkgs"] == "" {
		return false
	}
	node, ok := lock.Nodes[root.Inputs["nixpkgs"]]
	if !ok {
		return false
	}
	locked := node.Locked
	rev, err := hex.DecodeString(locked.Rev)
	return err == nil && len(rev) == 20 && locked.Type == "github" && locked.Owner == "NixOS" &&
		locked.Repo == "nixpkgs" && locked.NarHash != ""
}

func freshSource(
	ctx context.Context,
	base, system string,
	packages []manifest.Package,
	stderr io.Writer,
) (string, error) {
	root := filepath.Join(base, "dotm")
	instances := filepath.Join(root, "sources")
	if err := durableMkdir(instances); err != nil {
		return "", fmt.Errorf("create source instances: %w", err)
	}
	var path, id string
	for {
		var bytes [16]byte
		if _, err := rand.Read(bytes[:]); err != nil {
			return "", fmt.Errorf("create source identity: %w", err)
		}
		id = hex.EncodeToString(bytes[:])
		path = filepath.Join(instances, id)
		if err := os.Mkdir(path, 0o700); os.IsExist(err) {
			continue
		} else if err != nil {
			return "", fmt.Errorf("create source instance: %w", err)
		}
		break
	}
	if err := prepareSource(ctx, path, system, packages, true, stderr); err != nil {
		return path, err
	}
	state, err := inspectSource(path)
	if err != nil {
		return path, err
	}
	if !state.Healthy {
		return path, fmt.Errorf("new source is unusable: %s", state.Reason)
	}
	// Publish before adding packages: an interrupted/failed add can be inspected
	// and retried against the same instance without adopting the old entries.
	if err := syncSource(path); err != nil {
		return path, err
	}
	if err := syncDirectory(instances); err != nil {
		return path, err
	}
	if err := durableReplace(filepath.Join(root, sourcePointerName), []byte(id+"\n"), 0o600); err != nil {
		return path, fmt.Errorf("publish current source: %w", err)
	}
	return path, nil
}

func syncSource(source string) error {
	for _, name := range []string{"flake.nix", "flake.lock"} {
		path := filepath.Join(source, name)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("source file %s is not a regular file", name)
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		syncErr, closeErr := file.Sync(), file.Close()
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return syncDirectory(source)
}
