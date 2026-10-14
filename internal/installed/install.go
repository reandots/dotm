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

	"dotm/internal/hooks"
	"dotm/internal/manifest"
)

// Install adds missing descriptors without upgrading or removing native entries.
// The returned report is freshly inspected after any attempted mutation, even
// on failure. An empty Profile means inspection itself failed.
func Install(
	ctx context.Context,
	desired manifest.Resolved,
	stderr io.Writer,
	options ConfigurationOptions,
) (report Report, resultErr error) {
	output := &operationOutput{Writer: stderr}
	stderr = output
	defer finishOperation(&report, &resultErr, output)
	ctx, release, err := beginOperation(ctx, stderr)
	if err != nil {
		return Report{}, err
	}
	defer release()
	before, err := Inspect(ctx, desired)
	if err != nil {
		return Report{}, err
	}
	base, err := stateHome()
	if err != nil {
		return before, err
	}
	state, err := readSourceState(base)
	if err != nil {
		return before, err
	}
	source := state.Path
	if !state.Healthy {
		source = ""
	}
	missing, retained, allowInit, err := installationPlan(before, desired, source)
	if err != nil {
		return before, err
	}
	var system string
	if len(missing) > 0 {
		system, err = nixSystem()
		if err != nil {
			return before, err
		}
	}
	if report, err := runHookPhase(ctx, desired, desired, hooks.Pre, before, stderr); err != nil {
		return report, operationError(SeverityInstallation, err)
	}
	if len(missing) == 0 {
		return configure(ctx, desired, before, stderr, options)
	}
	var operationErr error
	if state.Healthy {
		operationErr = prepareSource(ctx, source, system, retained, allowInit, stderr)
	} else {
		fmt.Fprintf(stderr, "Source recovery: %s; existing entries are external. "+
			"Installing all desired packages from current nixos-unstable in a new source.\n", state.Reason)
		source, operationErr = freshSource(ctx, base, system, desired.Packages, stderr)
	}
	if operationErr == nil {
		operationErr = addPackages(ctx, before.Profile, source, system, missing, stderr)
	}
	after, inspectErr := Inspect(ctx, desired)
	if inspectErr != nil {
		return Report{}, errors.Join(operationErr, fmt.Errorf("inspect after install attempt: %w", inspectErr))
	}
	if operationErr != nil {
		return after, operationError(SeverityInstallation, operationErr)
	}
	for _, app := range after.Applications {
		if !app.Ready {
			return after, operationError(SeverityInstallation, fmt.Errorf(
				"install finished but application %q is %s; inspect the profile before retrying",
				app.ID,
				app.State,
			))
		}
	}
	return configure(ctx, desired, after, stderr, options)
}

func installationPlan(
	before Report,
	desired manifest.Resolved,
	source string,
) (missing, retained []manifest.Package, allowInit bool, err error) {
	for _, app := range before.Applications {
		for _, p := range app.Packages {
			if p.State == "ambiguous" {
				return nil, nil, false, fmt.Errorf(
					"cannot install with ambiguous descriptor %s: %s",
					p.Descriptor,
					strings.Join(p.Entries, ", "),
				)
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
			return nil, nil, false, fmt.Errorf(
				"cannot regenerate source: native entry %q references it with an unrecognized descriptor",
				e.Name,
			)
		}
		packages[p.Key()] = p
	}
	for _, e := range before.Infrastructure {
		if p, ok := descriptor(e.Descriptor); ok {
			packages[p.Key()] = p
		}
	}
	if p, ok := descriptor(before.retainedDescriptor); ok {
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

func addPackages(
	ctx context.Context,
	profile, source, system string,
	packages []manifest.Package,
	stderr io.Writer,
) error {
	if len(packages) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(profile), 0o700); err != nil {
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
	retainMutationLease(ctx, cmd)
	cmd.Stdout, cmd.Stderr = stderr, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("nix profile add --profile %s: %w; existing entries were preserved; "+
			"if Nix reports a file conflict, explicitly remove the conflicting external entries and retry",
			profile, err)
	}
	return nil
}
