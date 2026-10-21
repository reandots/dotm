package chezmoi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"dotm/internal/pathutil"
)

// InventoryResult is a complete filesystem classification within supported roots.
type InventoryResult struct {
	Entries []InventoryEntry
}

// InventoryEntry describes a managed difference or unclassified filesystem entry.
type InventoryEntry struct {
	Path         string
	Type         string
	Local        byte
	Pending      byte
	Managed      bool
	Applications []string
}

var errInventoryAncestor = errors.New("destination ancestor is not a directory")

// Inventory consumes a passively validated source and a checked, refreshed policy.
// Native-only enumeration must not mistake profile exclusions for acknowledgements.
func Inventory(ctx context.Context, source string, policy Policy) (InventoryResult, error) {
	result := InventoryResult{}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	destination := policy.DestDir
	if !filepath.IsAbs(destination) || filepath.Clean(destination) != destination {
		return result, fmt.Errorf("inventory requires a normalized absolute destination: %q", destination)
	}
	roots, err := inventoryRoots()
	if err != nil {
		return result, err
	}
	for _, root := range roots {
		if !within(destination, root) {
			return result, fmt.Errorf("inventory root %q is outside chezmoi destination %q; "+
				"XDG paths are not remapped", root, destination)
		}
	}
	if err = checkDirectoryAncestors(destination); err != nil {
		return result, fmt.Errorf("inspect destination root: %w", err)
	}
	command := applyCommand{source: source}
	native := `{"dotm":{"nativeOnly":true}}`
	managedData, err := policyCommand(ctx, command, "--override-data", native, "managed", "--nul-path-separator")
	if err != nil {
		return result, err
	}
	managed := make(map[string]bool)
	for _, raw := range parseNULPaths(managedData) {
		path := candidatePath(raw, destination)
		if !within(destination, path) {
			return result, fmt.Errorf("managed candidate outside destination: %q", path)
		}
		if inInventoryRoots(roots, path) {
			managed[path] = true
		}
	}
	dump, err := policyCommand(ctx, command, "dump", "--format=json")
	if err != nil {
		return result, err
	}
	var state map[string]targetEntry
	if err = json.Unmarshal(dump, &state); err != nil {
		return result, fmt.Errorf("decode chezmoi target state: %w", err)
	}
	// Any line-breaking target can forge a second status record, even for another path.
	// In that case attribute every selected entry through a separate explicit query.
	perEntry := false
	selected := make(map[string]InventoryEntry)
	for relative, entry := range state {
		path := filepath.Join(destination, relative)
		if filepath.IsAbs(relative) || path == destination || !within(destination, path) {
			return result, fmt.Errorf("invalid chezmoi target path %q", relative)
		}
		perEntry = perEntry || strings.ContainsAny(relative, "\r\n")
		if inInventoryRoots(roots, path) {
			owners := inventoryOwners(policy, path)
			if len(owners) != 0 {
				selected[path] = InventoryEntry{Path: path, Type: entry.Type, Applications: owners, Managed: true}
			}
		}
	}
	statusData, err := policyCommand(ctx, command, "status")
	if err != nil {
		return result, err
	}
	var statuses map[string][2]byte
	if !perEntry {
		statuses = parseStatusPaths(statusData, destination)
	}
	for path, entry := range selected {
		info, statErr := lstatNoSymlinkAncestors(destination, path)
		if errors.Is(statErr, errInventoryAncestor) {
			continue
		}
		if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
			return result, fmt.Errorf("inspect %q: %w", path, statErr)
		}
		columns := [2]byte{' ', ' '}
		if perEntry {
			data, statusErr := policyCommand(ctx, command,
				"status", "--recursive=false", "--parent-dirs=false", "--", path)
			if statusErr != nil {
				return result, statusErr
			}
			columns[0], columns[1] = statusColumns(data)
		} else if status, found := statuses[path]; found {
			columns = status
		}
		if columns == [2]byte{' ', ' '} {
			continue
		}
		kind := entry.Type
		if kind == "dir" {
			kind = "directory"
		}
		if statErr == nil {
			kind = fileType(info)
		}
		entry.Type, entry.Local, entry.Pending = kind, columns[0], columns[1]
		result.Entries = append(result.Entries, entry)
	}
	acks := inventoryAcknowledgements(policy)
	// Existing native-managed leaves outside selected configs are unclassified too.
	unknown := make(map[string]bool)
	for path := range managed {
		if _, chosen := selected[path]; chosen || acknowledged(path, acks) {
			continue
		}
		info, statErr := lstatNoSymlinkAncestors(destination, path)
		if errors.Is(statErr, fs.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return result, fmt.Errorf("inspect %q: %w", path, statErr)
		}
		if !info.IsDir() {
			unknown[path] = true
		}
	}
	boundaries, err := inventoryBoundaries(policy)
	if err != nil {
		return result, err
	}
	var collect func([]string) error
	collect = func(paths []string) error {
		if len(paths) == 0 {
			return nil
		}
		args := []string{
			"--override-data", native, "unmanaged",
			"--nul-path-separator", "--path-style=absolute", "--",
		}
		data, queryErr := policyCommand(ctx, command, append(args, paths...)...)
		if queryErr != nil {
			return queryErr
		}
		for _, raw := range parseNULPaths(data) {
			path := candidatePath(raw, destination)
			if !within(destination, path) || !inInventoryRoots(roots, path) {
				return fmt.Errorf("unmanaged candidate outside inventory scope: %q", path)
			}
			if acknowledged(path, acks) || managed[path] {
				continue
			}
			info, statErr := lstatNoSymlinkAncestors(destination, path)
			if statErr != nil {
				return fmt.Errorf("inspect %q: %w", path, statErr)
			}
			structural := boundaries[path] || hasAckBelow(path, acks)
			if !info.IsDir() || !structural {
				unknown[path] = true
				continue
			}
			children, readErr := os.ReadDir(path)
			if readErr != nil {
				return fmt.Errorf("enumerate %q: %w", path, readErr)
			}
			next := make([]string, 0, len(children))
			for _, child := range children {
				childPath := filepath.Join(path, child.Name())
				if !acknowledged(childPath, acks) {
					next = append(next, childPath)
				}
			}
			// Explicit children preserve native ignores hidden by unmanaged compaction.
			if collectErr := collect(next); collectErr != nil {
				return collectErr
			}
		}
		return nil
	}
	if err = collect(roots); err != nil {
		return result, err
	}
	if err = compactInventoryUnknown(destination, unknown, boundaries, managed, acks); err != nil {
		return result, err
	}
	for path := range unknown {
		info, statErr := lstatNoSymlinkAncestors(destination, path)
		if statErr != nil {
			return result, fmt.Errorf("inspect %q: %w", path, statErr)
		}
		result.Entries = append(result.Entries, InventoryEntry{Path: path, Type: fileType(info)})
	}
	sort.Slice(result.Entries, func(i, j int) bool { return result.Entries[i].Path < result.Entries[j].Path })
	return result, nil
}

func inventoryBoundaries(policy Policy) (map[string]bool, error) {
	destination := policy.DestDir
	boundaries := map[string]bool{destination: true}
	for _, value := range []string{"~", "$XDG_CONFIG_HOME", "$XDG_DATA_HOME", "$XDG_STATE_HOME", "$XDG_CACHE_HOME"} {
		path, err := pathutil.Target(value)
		if err != nil {
			return nil, err
		}
		boundaries[path] = true
	}
	for _, declaration := range policy.Input.Files.Declarations {
		if declaration.Field != "configs" || !declaration.Selected {
			continue
		}
		for parent := filepath.Dir(declaration.Path); within(destination, parent); parent = filepath.Dir(parent) {
			boundaries[parent] = true
			if parent == destination {
				break
			}
		}
		boundaries[declaration.Path] = true
	}
	return boundaries, nil
}

// Nonselected native trees group visible unknown entries despite native ignores.
// Selected declarations, inventory roots, and dotm acknowledgements remain boundaries.
func compactInventoryUnknown(
	destination string, unknown, boundaries, managed map[string]bool, acks []string,
) error {
	candidates := make(map[string]bool)
	for path := range unknown {
		parent := filepath.Dir(path)
		for within(destination, parent) && !boundaries[parent] {
			if candidates[parent] {
				break
			}
			candidates[parent] = true
			parent = filepath.Dir(parent)
		}
	}
	paths := make([]string, 0, len(candidates))
	for path := range candidates {
		paths = append(paths, path)
	}
	// Descendants precede their parents in reverse lexical order.
	sort.Sort(sort.Reverse(sort.StringSlice(paths)))
	for _, path := range paths {
		info, err := lstatNoSymlinkAncestors(destination, path)
		if err != nil {
			return fmt.Errorf("inspect %q: %w", path, err)
		}
		if !info.IsDir() {
			continue
		}
		children, err := os.ReadDir(path)
		if err != nil {
			return fmt.Errorf("enumerate %q: %w", path, err)
		}
		complete := len(children) != 0
		groupNative := managed[path] && !hasAckBelow(path, acks)
		for _, child := range children {
			if !unknown[filepath.Join(path, child.Name())] && !groupNative {
				complete = false
				break
			}
		}
		if complete {
			for _, child := range children {
				delete(unknown, filepath.Join(path, child.Name()))
			}
			unknown[path] = true
		}
	}
	return nil
}

func inventoryOwners(policy Policy, path string) []string {
	var owners []string
	for _, declaration := range policy.Input.Files.Declarations {
		if declaration.Field == "configs" && declaration.Selected && within(declaration.Path, path) {
			owners = appendOwner(owners, declaration.Application)
		}
	}
	return owners
}

func inventoryAcknowledgements(policy Policy) []string {
	var paths []string
	for _, exclusion := range policy.Exclusions {
		origin := exclusion.Origin
		if origin.Field == "unmanaged" && (origin.Application == "" || origin.Selected) {
			paths = append(paths, exclusion.Root)
		}
	}
	return paths
}

func acknowledged(path string, acks []string) bool {
	return inInventoryRoots(acks, path)
}

func hasAckBelow(path string, acks []string) bool {
	for _, ack := range acks {
		if path != ack && within(path, ack) {
			return true
		}
	}
	return false
}

func inInventoryRoots(roots []string, path string) bool {
	for _, root := range roots {
		if within(root, path) {
			return true
		}
	}
	return false
}

func lstatNoSymlinkAncestors(root, path string) (fs.FileInfo, error) {
	if err := checkDirectoryAncestors(root); err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || !within(root, path) {
		return nil, fmt.Errorf("path %q is outside root %q", path, root)
	}
	current := root
	parts := strings.Split(relative, string(filepath.Separator))
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if statErr != nil {
			return nil, statErr
		}
		if i == len(parts)-1 {
			return info, nil
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, errors.Join(fs.ErrNotExist, errInventoryAncestor)
		}
	}
	return os.Lstat(root)
}

func checkDirectoryAncestors(path string) error {
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.Join(fs.ErrNotExist, errInventoryAncestor)
		}
		if filepath.Dir(current) == current {
			return nil
		}
	}
}

func candidatePath(path, root string) string {
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	return filepath.Clean(path)
}

func parseNULPaths(data []byte) []string {
	return strings.FieldsFunc(string(data), func(r rune) bool { return r == 0 })
}

func statusColumns(data []byte) (byte, byte) {
	if len(data) < 2 {
		return ' ', ' '
	}
	return data[0], data[1]
}

func parseStatusPaths(data []byte, destination string) map[string][2]byte {
	statuses := make(map[string][2]byte)
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if len(line) >= 4 && line[2] == ' ' {
			statuses[candidatePath(line[3:], destination)] = [2]byte{line[0], line[1]}
		}
	}
	return statuses
}

func fileType(info fs.FileInfo) string {
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return "symlink"
	case info.IsDir():
		return "directory"
	case info.Mode().IsRegular():
		return "file"
	default:
		return "other"
	}
}

func inventoryRoots() ([]string, error) {
	var roots []string
	for _, value := range []string{"~", "$XDG_CONFIG_HOME", "$XDG_DATA_HOME", "$XDG_STATE_HOME", "$XDG_CACHE_HOME"} {
		path, err := pathutil.Target(value)
		if err != nil {
			return nil, err
		}
		if inInventoryRoots(roots, path) {
			continue
		}
		kept := roots[:0]
		for _, root := range roots {
			if !within(path, root) {
				kept = append(kept, root)
			}
		}
		roots = append(kept, path)
	}
	return roots, nil
}
