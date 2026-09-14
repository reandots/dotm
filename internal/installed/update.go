package installed

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"

	"dotm/internal/manifest"
)

// Update advances the machine-local source, upgrades installed desired entries,
// and adds missing descriptors. Extras are never selected for upgrade or removal.
// Source advancement and the two profile batches are separate commits. A failure
// returns freshly inspected state, not an assumed rollback.
func Update(ctx context.Context, desired manifest.Resolved, stderr io.Writer) (Report, error) {
	before, err := Inspect(ctx, desired)
	if err != nil {
		return Report{}, err
	}
	if len(desired.Packages) == 0 {
		fmt.Fprintln(stderr, "Update: no desired packages; source and profile unchanged.")
		return before, nil
	}
	base, err := stateHome()
	if err != nil {
		return before, err
	}
	source := filepath.Join(base, "dotm", "source")
	missing, retained, allowInit, err := installationPlan(before, desired, source)
	if err != nil {
		return before, err
	}
	names, err := updateTargets(before, desired, source)
	if err != nil {
		return before, err
	}
	system, err := nixSystem()
	if err != nil {
		return before, err
	}
	lockPath := filepath.Join(source, "flake.lock")
	oldLock, err := os.ReadFile(lockPath)
	if err != nil && !os.IsNotExist(err) {
		return before, fmt.Errorf("read source lock before update: %w", err)
	}

	_, operationErr := writeSource(source, system, retained, allowInit)
	if operationErr == nil {
		cmd := exec.CommandContext(ctx, "nix", "flake", "update", "nixpkgs", "--flake", "path:"+source)
		cmd.Stdout, cmd.Stderr = stderr, stderr
		if err := cmd.Run(); err != nil {
			operationErr = fmt.Errorf("advance source lock: %w", err)
		}
	}
	if operationErr == nil && len(names) > 0 {
		args := []string{"profile", "upgrade", "--profile", before.Profile, "--no-update-lock-file", "--"}
		args = append(args, names...)
		cmd := exec.CommandContext(ctx, "nix", args...)
		cmd.Stdout, cmd.Stderr = stderr, stderr
		if err := cmd.Run(); err != nil {
			operationErr = fmt.Errorf("upgrade desired native entries: %w", err)
		}
	}
	if operationErr == nil {
		operationErr = addPackages(ctx, before.Profile, source, system, missing, stderr)
	}

	newLock, lockErr := os.ReadFile(lockPath)
	lockState := "unchanged"
	switch {
	case os.IsNotExist(lockErr):
		lockState, lockErr = "absent", nil
	case lockErr != nil:
		lockState = "unavailable"
	case oldLock == nil:
		lockState = "initialized"
	case !bytes.Equal(oldLock, newLock):
		lockState = "changed"
	}
	fmt.Fprintf(stderr, "Source lock: %s.\n", lockState)
	if lockErr != nil {
		operationErr = errors.Join(operationErr, fmt.Errorf("read source lock after update attempt: %w", lockErr))
	}
	after, inspectErr := Inspect(ctx, desired)
	if inspectErr != nil {
		return Report{}, errors.Join(operationErr, fmt.Errorf("inspect after update attempt: %w", inspectErr))
	}
	profileState, packageState := "unchanged", "unchanged"
	if !reflect.DeepEqual(before.Entries, after.Entries) {
		profileState = "changed"
	}
	if packagePathsChanged(before, after) {
		packageState = "changed"
	}
	fmt.Fprintf(stderr, "Profile: %s; package store paths: %s.\n", profileState, packageState)
	if operationErr != nil {
		return after, fmt.Errorf("update failed; source/profile changes were not rolled back: %w", operationErr)
	}
	for _, app := range after.Applications {
		if app.State != "installed" {
			return after, fmt.Errorf("update finished but application %q is %s; inspect the profile before retrying", app.ID, app.State)
		}
	}
	if !reflect.DeepEqual(before.Extras, after.Extras) {
		return after, fmt.Errorf("extras changed during update; inspect the profile before retrying")
	}
	return after, nil
}

func updateTargets(before Report, desired manifest.Resolved, source string) ([]string, error) {
	wanted := make(map[string]bool, len(desired.Packages))
	for _, p := range desired.Packages {
		wanted[p.Key()] = true
	}
	var names []string
	for _, entry := range before.Entries {
		if !entry.Active || !wanted[entry.Descriptor] {
			continue
		}
		// Inspection recognizes direct nixpkgs entries, but their native recipes
		// can be pinned to another revision. Never claim the local lock updates
		// those entries, nor remove/re-add them non-atomically to change sources.
		if entry.OriginalURL != "path:"+source || !entry.Updateable {
			return nil, fmt.Errorf("cannot update desired entry %q from %q using the machine-local source lock; no changes made", entry.Name, entry.OriginalURL)
		}
		names = append(names, entry.Name)
	}
	return names, nil
}

func packagePathsChanged(before, after Report) bool {
	if len(before.Entries) != len(after.Entries) {
		return true
	}
	for i, entry := range before.Entries {
		if entry.Name != after.Entries[i].Name || !slices.Equal(entry.StorePaths, after.Entries[i].StorePaths) {
			return true
		}
	}
	return false
}
