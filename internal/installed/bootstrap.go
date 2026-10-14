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

	"dotm/internal/chezmoi"
	"dotm/internal/manifest"
)

// BootstrapEffect records a completed resource change, including on failure.
// Successful package acquisition is not rolled back by later setup stages.
type BootstrapEffect struct {
	Resource string
	Path     string
	Action   string
}

type BootstrapResult struct {
	Executable string
	Source     string
	Retained   bool
	Report     Report
	Effects    []BootstrapEffect
}

// BootstrapChezmoi obtains only the infrastructure backend, never applications
// or their hooks/configurations. Inputs must already be validated by the caller.
// Hold the caller's mutation context through subsequent setup stages and route
// every chezmoi call with chezmoi.WithExecutable(ctx, result.Executable).
func BootstrapChezmoi(ctx context.Context, stderr io.Writer) (result BootstrapResult, resultErr error) {
	if err := CheckPendingRecovery(); err != nil {
		return result, err
	}
	ctx, release, err := BeginMutation(ctx)
	if err != nil {
		return result, err
	}
	defer release()
	if pendingErr := CheckPendingRecovery(); pendingErr != nil {
		return result, pendingErr
	}
	if _, gitErr := exec.LookPath("git"); gitErr != nil {
		return result, fmt.Errorf("git is required for checkout acquisition; install Git before setup: %w", gitErr)
	}
	base, err := stateHome()
	if err != nil {
		return result, err
	}
	role, err := readInfrastructure(base)
	if err != nil {
		return result, err
	}
	before, err := Inspect(ctx, manifest.Resolved{})
	if err != nil {
		return result, err
	}
	result.Report = before
	// Compare resource bytes, not classification flags: recording a role for
	// an already installed descriptor does not itself change the native profile.
	watched := make(map[string][]byte)
	watch := func(path string) {
		if _, exists := watched[path]; !exists {
			watched[path], _ = os.ReadFile(path)
		}
	}
	watch(filepath.Join(base, "dotm", "infrastructure.json"))
	watch(filepath.Join(base, "dotm", sourcePointerName))
	profileManifest := filepath.Join(before.Profile, "manifest.json")
	watch(profileManifest)
	defer func() {
		// Stable order keeps partial reports deterministic.
		paths := []string{
			profileManifest,
			filepath.Join(base, "dotm", sourcePointerName),
			filepath.Join(result.Source, "flake.nix"), filepath.Join(result.Source, "flake.lock"),
			filepath.Join(base, "dotm", "infrastructure.json"),
		}
		for _, path := range paths {
			previous, ok := watched[path]
			if !ok {
				continue
			}
			current, readErr := os.ReadFile(path)
			if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				resultErr = errors.Join(resultErr, fmt.Errorf("inspect bootstrap effect %s: %w", path, readErr))
				continue
			}
			if bytes.Equal(previous, current) {
				continue
			}
			action := "changed"
			if previous == nil {
				action = "created"
			}
			resource := "state"
			if path == profileManifest {
				resource = "profile"
			}
			result.Effects = append(result.Effects, BootstrapEffect{resource, path, action})
		}
	}()

	if role.Descriptor != "" && len(before.Infrastructure) != 0 {
		result.Source = role.Source
		return finishBootstrap(ctx, base, role, before.Infrastructure, result)
	}
	if role.Descriptor == "" {
		candidate, candidateErr := pathChezmoi()
		if candidateErr != nil {
			return result, candidateErr
		}
		if candidate != "" {
			result.Executable = candidate
			return result, chezmoi.ProbeExecutable(ctx, candidate)
		}
	}

	state, err := readSourceState(base)
	if err != nil {
		return result, err
	}
	source := state.Path
	if !state.Healthy {
		source = ""
	}
	pkg := manifest.Package{Attr: infrastructureDescriptor}
	missing, retained, allowInit, err := installationPlan(
		before, manifest.Resolved{Packages: []manifest.Package{pkg}}, source,
	)
	if err != nil {
		return result, err
	}
	system, err := nixSystem()
	if err != nil {
		return result, err
	}
	active := 0
	for _, entry := range before.Entries {
		if entry.Active && entry.Descriptor == infrastructureDescriptor {
			active++
			if entry.AttrPath != "packages."+system+"."+managedAttribute(pkg) ||
				entry.OriginalURL != "path:"+source {
				return result, fmt.Errorf("existing chezmoi descriptor is not from the canonical generated source; " +
					"repair its profile/session exposure before setup")
			}
		}
	}
	if active > 1 {
		return result, fmt.Errorf("multiple active chezmoi descriptors; repair the profile before setup")
	}
	if state.Healthy {
		result.Source = source
		watch(filepath.Join(source, "flake.nix"))
		watch(filepath.Join(source, "flake.lock"))
		if prepareErr := prepareSource(ctx, source, system, retained, allowInit, stderr); prepareErr != nil {
			return result, prepareErr
		}
	} else {
		// freshSource returns its allocated path even if initialization fails.
		source, err = freshSource(ctx, base, system, retained, stderr)
		result.Source = source
		if source != "" {
			watched[filepath.Join(source, "flake.nix")] = nil
			watched[filepath.Join(source, "flake.lock")] = nil
		}
		if err != nil {
			return result, err
		}
	}
	role = infrastructureState{Descriptor: infrastructureDescriptor, Source: source, Pending: true}
	// Publish intent before add. Canonical profile evidence reconciles an add
	// that completed before role finalization (including a killed parent).
	if err := writeInfrastructure(base, role); err != nil {
		return result, err
	}
	addErr := addPackages(ctx, before.Profile, source, system, missing, stderr)
	after, inspectErr := Inspect(context.WithoutCancel(ctx), manifest.Resolved{})
	if inspectErr != nil {
		result.Report = Report{}
		return result, errors.Join(addErr, inspectErr)
	}
	result.Report = after
	if addErr != nil {
		return result, addErr
	}
	return finishBootstrap(ctx, base, role, after.Infrastructure, result)
}

func finishBootstrap(
	ctx context.Context, base string, role infrastructureState, entries []Entry, result BootstrapResult,
) (BootstrapResult, error) {
	if len(entries) != 1 || !entries[0].Active {
		return result, fmt.Errorf("retained chezmoi descriptor needs exactly one active native entry; " +
			"repair the standard Nix profile and retry setup")
	}
	var executable string
	for _, store := range entries[0].StorePaths {
		candidate := filepath.Join(store, "bin", "chezmoi")
		if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return result, fmt.Errorf("inspect retained chezmoi: %w", err)
		}
		resolved, err := usableChezmoi(candidate)
		if err != nil {
			return result, err
		}
		if executable != "" && executable != resolved {
			return result, fmt.Errorf("retained descriptor exposes multiple chezmoi executables; " +
				"repair profile and retry")
		}
		executable = resolved
	}
	if executable == "" {
		return result, fmt.Errorf("retained chezmoi entry has no executable; repair profile and retry setup")
	}
	result.Executable, result.Retained = executable, true
	if err := chezmoi.ProbeExecutable(ctx, executable); err != nil {
		return result, err
	}
	if role.Pending {
		role.Pending = false
		if err := writeInfrastructure(base, role); err != nil {
			return result, fmt.Errorf("backend acquired; role finalization pending, retry setup to reconcile: %w", err)
		}
	}
	return result, nil
}

// Inspect the first existing PATH candidate rather than skipping an unusable
// executable and silently installing or selecting a different backend.
func pathChezmoi() (string, error) {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		candidate := filepath.Join(dir, "chezmoi")
		// #nosec G703 -- Intentionally inspect each caller PATH directory, not a confined filesystem tree.
		if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return "", fmt.Errorf("inspect PATH chezmoi: %w", err)
		}
		return usableChezmoi(candidate)
	}
	return "", nil
}

func usableChezmoi(candidate string) (string, error) {
	resolved, err := filepath.EvalSymlinks(candidate)
	if err == nil {
		resolved, err = filepath.Abs(resolved)
	}
	if err == nil {
		var info os.FileInfo
		// #nosec G703 -- Validate the actual backend executable selected from PATH or canonical profile evidence.
		info, err = os.Stat(resolved)
		if err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0) {
			err = fmt.Errorf("not an executable regular file")
		}
	}
	if err != nil {
		return "", fmt.Errorf("unusable chezmoi candidate %q; repair or remove it before setup: %w", candidate, err)
	}
	return resolved, nil
}
