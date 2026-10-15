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
	"sort"

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
	Effects    []BootstrapEffect
}

// FindChezmoi discovers and compatibility-checks a reusable chezmoi executable.
func FindChezmoi(ctx context.Context) (string, error) {
	candidate, err := pathChezmoi()
	if err != nil {
		return "", err
	}
	if candidate == "" {
		candidate, err = profileChezmoi()
	}
	if err != nil || candidate == "" {
		return candidate, err
	}
	if err := chezmoi.ProbeExecutable(ctx, candidate); err != nil {
		return "", err
	}
	return candidate, nil
}

func profileChezmoi() (string, error) {
	base, err := stateHome()
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(base, "nix", "profile", "bin", "chezmoi")
	if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", fmt.Errorf("inspect profile chezmoi: %w", err)
	}
	return usableChezmoi(candidate)
}

func resolveOperationalChezmoi(ctx context.Context) (string, error) {
	candidate, err := profileChezmoi()
	if err != nil {
		return "", err
	}
	if candidate == "" {
		candidate, err = pathChezmoi()
	}
	if err != nil || candidate == "" {
		return candidate, err
	}
	if err := chezmoi.ProbeExecutable(ctx, candidate); err != nil {
		return "", err
	}
	return candidate, nil
}

// BootstrapChezmoi installs only the caller-resolved chezmoi application closure.
func BootstrapChezmoi(
	ctx context.Context, desired manifest.Resolved, stderr io.Writer,
) (result BootstrapResult, resultErr error) {
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
	candidate, err := FindChezmoi(ctx)
	if err != nil {
		return result, err
	}
	if candidate != "" {
		result.Executable = candidate
		return result, nil
	}
	if _, gitErr := exec.LookPath("git"); gitErr != nil {
		return result, fmt.Errorf("git is required for checkout acquisition; install Git before setup: %w", gitErr)
	}
	base, err := stateHome()
	if err != nil {
		return result, err
	}
	profile := filepath.Join(base, "nix", "profile", "manifest.json")
	pointer := filepath.Join(base, "dotm", sourcePointerName)
	watched := map[string][]byte{profile: nil, pointer: nil}
	for path := range watched {
		watched[path], _ = os.ReadFile(path)
	}
	if state, stateErr := readSourceState(base); stateErr == nil && state.Healthy {
		for _, name := range []string{"flake.nix", "flake.lock"} {
			path := filepath.Join(state.Path, name)
			watched[path], _ = os.ReadFile(path)
		}
	}
	sourceDir := filepath.Join(base, "dotm", "sources")
	knownSources := make(map[string]bool)
	if dirs, readErr := os.ReadDir(sourceDir); readErr == nil {
		for _, dir := range dirs {
			if dir.IsDir() {
				knownSources[filepath.Join(sourceDir, dir.Name())] = true
			}
		}
	}
	defer func() {
		if dirs, readErr := os.ReadDir(sourceDir); readErr == nil {
			for _, dir := range dirs {
				path := filepath.Join(sourceDir, dir.Name())
				if !dir.IsDir() || knownSources[path] {
					continue
				}
				for _, name := range []string{"flake.nix", "flake.lock"} {
					watched[filepath.Join(path, name)] = nil
				}
			}
		}
		paths := make([]string, 0, len(watched))
		for path := range watched {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			after, readErr := os.ReadFile(path)
			if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				resultErr = errors.Join(resultErr, fmt.Errorf("inspect bootstrap effect %s: %w", path, readErr))
				continue
			}
			if bytes.Equal(watched[path], after) {
				continue
			}
			action, resource := "changed", "state"
			if watched[path] == nil {
				action = "created"
			}
			if path == profile {
				resource = "profile"
			}
			result.Effects = append(result.Effects, BootstrapEffect{Resource: resource, Path: path, Action: action})
		}
	}()
	if _, installErr := installPackages(ctx, desired, stderr); installErr != nil {
		return result, installErr
	}
	result.Executable, err = resolveOperationalChezmoi(ctx)
	if err != nil {
		return result, err
	}
	if result.Executable == "" {
		return result, fmt.Errorf("chezmoi installation completed but no executable was found")
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
