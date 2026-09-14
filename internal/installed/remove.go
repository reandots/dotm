package installed

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"reflect"
	"sort"
	"strings"

	"dotm/internal/manifest"
)

type removalUnit struct {
	application string
	entries     []Entry
}

// RemoveExtras collects all confirmations before removing exact native entry
// names. After a native attempt, the returned report is freshly inspected even
// on failure. An empty Profile means inspection itself failed.
func RemoveExtras(ctx context.Context, catalog *manifest.Data, desired manifest.Resolved, yes bool, in io.Reader, stderr io.Writer) (Report, error) {
	before, err := Inspect(ctx, desired)
	if err != nil {
		return Report{}, err
	}
	units := removalUnits(before, catalog, desired)
	names, err := confirmRemovals(units, yes, in, stderr)
	if err != nil || len(names) == 0 {
		return before, err
	}
	// Do not apply approvals to entries replaced while the user was answering.
	current, err := Inspect(ctx, desired)
	if err != nil {
		return Report{}, err
	}
	if !reflect.DeepEqual(before.Entries, current.Entries) {
		return current, fmt.Errorf("profile changed during removal planning; rerun remove-extras to review the current entries")
	}
	args := []string{"profile", "remove", "--profile", before.Profile, "--"}
	args = append(args, names...)
	cmd := exec.CommandContext(ctx, "nix", args...)
	cmd.Stdout, cmd.Stderr = stderr, stderr
	var operationErr error
	if err := cmd.Run(); err != nil {
		operationErr = fmt.Errorf("nix profile remove --profile %s: %w; no automatic rollback was attempted", before.Profile, err)
	}
	after, inspectErr := Inspect(ctx, desired)
	if inspectErr != nil {
		return Report{}, errors.Join(operationErr, fmt.Errorf("inspect after removal attempt: %w", inspectErr))
	}
	if operationErr != nil {
		return after, operationErr
	}
	removed := make(map[string]bool, len(names))
	for _, name := range names {
		removed[name] = true
	}
	for _, entry := range after.Entries {
		if removed[entry.Name] {
			return after, fmt.Errorf("removal finished but approved entry %q remains; inspect the profile before retrying", entry.Name)
		}
	}
	return after, nil
}

func removalUnits(before Report, catalog *manifest.Data, desired manifest.Resolved) []removalUnit {
	wantedApps := make(map[string]bool, len(desired.Applications))
	wantedPackages := make(map[string]bool, len(desired.Packages))
	for _, app := range desired.Applications {
		wantedApps[app.ID] = true
	}
	for _, p := range desired.Packages {
		wantedPackages[p.Key()] = true
	}
	byDescriptor := make(map[string][]Entry)
	for _, entry := range before.Extras {
		if entry.Descriptor != "" && !wantedPackages[entry.Descriptor] {
			byDescriptor[entry.Descriptor] = append(byDescriptor[entry.Descriptor], entry)
		}
	}
	var units []removalUnit
	grouped := make(map[string]bool)
	for _, id := range catalog.ApplicationIDs() {
		if wantedApps[id] {
			continue
		}
		unit := removalUnit{application: id}
		for _, p := range catalog.Apps[id].Packages {
			for _, entry := range byDescriptor[p.Key()] {
				unit.entries = append(unit.entries, entry)
				grouped[entry.Name] = true
			}
		}
		if len(unit.entries) > 0 {
			sort.Slice(unit.entries, func(i, j int) bool { return unit.entries[i].Name < unit.entries[j].Name })
			units = append(units, unit)
		}
	}
	for _, entry := range before.Extras {
		if !grouped[entry.Name] {
			units = append(units, removalUnit{entries: []Entry{entry}})
		}
	}
	return units
}

func confirmRemovals(units []removalUnit, yes bool, in io.Reader, out io.Writer) ([]string, error) {
	if len(units) == 0 {
		return nil, nil
	}
	if _, err := fmt.Fprintln(out, "Removal candidates (shared entries require approval of every listed application; nothing changes until all answers are collected):"); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(in)
	approved := make(map[string]bool)
	for _, unit := range units {
		for _, entry := range unit.entries {
			approved[entry.Name] = true
		}
	}
	for _, unit := range units {
		if unit.application != "" {
			if _, err := fmt.Fprintf(out, "Application %q:\n", unit.application); err != nil {
				return nil, err
			}
		} else if _, err := fmt.Fprintln(out, "Native entry outside extra application groups:"); err != nil {
			return nil, err
		}
		for _, entry := range unit.entries {
			descriptor := entry.Descriptor
			if descriptor == "" {
				descriptor = "unknown"
			}
			if _, err := fmt.Fprintf(out, "  %s [%s; active=%t] store paths: %s\n", entry.Name, descriptor, entry.Active, strings.Join(entry.StorePaths, ", ")); err != nil {
				return nil, err
			}
		}
		accept := yes
		if !yes {
			var err error
			accept, err = confirmRemoval(reader, out)
			if err != nil {
				return nil, err
			}
		}
		if !accept {
			for _, entry := range unit.entries {
				approved[entry.Name] = false
			}
		}
	}
	var names []string
	for name, accept := range approved {
		if accept {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		if _, err := fmt.Fprintln(out, "No entries approved for removal."); err != nil {
			return nil, err
		}
	} else if _, err := fmt.Fprintf(out, "Removing native entries: %s\n", strings.Join(names, ", ")); err != nil {
		return nil, err
	}
	return names, nil
}

func confirmRemoval(reader *bufio.Reader, out io.Writer) (bool, error) {
	for {
		if _, err := fmt.Fprint(out, "Remove? [y/N/q] "); err != nil {
			return false, err
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			return false, fmt.Errorf("removal cancelled before mutation: read confirmation: %w", err)
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			return true, nil
		case "", "n", "no":
			return false, nil
		case "q", "quit":
			return false, fmt.Errorf("removal cancelled before mutation")
		default:
			if _, err := fmt.Fprintln(out, "Answer y/yes, n/no (default), or q/quit to cancel."); err != nil {
				return false, err
			}
		}
	}
}
