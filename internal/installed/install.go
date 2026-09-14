package installed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"dotm/internal/manifest"
)

// Install adds missing descriptors without upgrading or removing native entries.
// The returned report is freshly inspected after any attempted mutation, even
// on failure. An empty Profile means inspection itself failed.
func Install(ctx context.Context, desired manifest.Resolved, stderr io.Writer) (Report, error) {
	before, err := Inspect(ctx, desired)
	if err != nil {
		return Report{}, err
	}
	base, err := stateHome()
	if err != nil {
		return before, err
	}
	source := filepath.Join(base, "dotm", "source")
	missing, retained, allowInit, err := installationPlan(before, desired, source)
	if err != nil || len(missing) == 0 {
		return before, err
	}
	system, err := nixSystem()
	if err != nil {
		return before, err
	}
	operationErr := prepareSource(ctx, source, system, retained, allowInit, stderr)
	if operationErr == nil {
		operationErr = addPackages(ctx, before.Profile, source, system, missing, stderr)
	}
	after, inspectErr := Inspect(ctx, desired)
	if inspectErr != nil {
		return Report{}, errors.Join(operationErr, fmt.Errorf("inspect after install attempt: %w", inspectErr))
	}
	if operationErr != nil {
		return after, operationErr
	}
	for _, app := range after.Applications {
		if app.State != "installed" {
			return after, fmt.Errorf("install finished but application %q is %s; inspect the profile before retrying", app.ID, app.State)
		}
	}
	return after, nil
}

func installationPlan(before Report, desired manifest.Resolved, source string) (missing, retained []manifest.Package, allowInit bool, err error) {
	for _, app := range before.Applications {
		for _, p := range app.Packages {
			if p.State == "ambiguous" {
				return nil, nil, false, fmt.Errorf("cannot install with ambiguous descriptor %s: %s", p.Descriptor, strings.Join(p.Entries, ", "))
			}
		}
	}
	active := make(map[string]bool)
	packages := make(map[string]manifest.Package)
	allowInit = true
	for _, e := range before.Entries {
		if e.Active && e.Descriptor != "" {
			active[e.Descriptor] = true
		}
		if e.OriginalURL != "path:"+source {
			continue
		}
		allowInit = false
		p, ok := descriptor(e.Descriptor)
		if !ok {
			return nil, nil, false, fmt.Errorf("cannot regenerate source: native entry %q references it with an unrecognized descriptor", e.Name)
		}
		packages[p.Key()] = p
	}
	for _, p := range desired.Packages {
		packages[p.Key()] = p
		if !active[p.Key()] {
			missing = append(missing, p)
		}
	}
	for _, p := range packages {
		retained = append(retained, p)
	}
	sort.Slice(retained, func(i, j int) bool { return retained[i].Key() < retained[j].Key() })
	return missing, retained, allowInit, nil
}

func addPackages(ctx context.Context, profile, source, system string, packages []manifest.Package, stderr io.Writer) error {
	if len(packages) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(profile), 0700); err != nil {
		return err
	}
	args := []string{"profile", "add", "--profile", profile, "--no-update-lock-file"}
	for _, p := range packages {
		ref := "path:" + source + "#packages." + system + "." + managedAttribute(p)
		if len(p.Outputs) > 0 {
			ref += "^" + strings.Join(p.Outputs, ",")
		}
		args = append(args, ref)
	}
	cmd := exec.CommandContext(ctx, "nix", args...)
	cmd.Stdout, cmd.Stderr = stderr, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("nix profile add --profile %s: %w", profile, err)
	}
	return nil
}
