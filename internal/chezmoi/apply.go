package chezmoi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"dotm/internal/manifest"
)

// ApplyStatus describes configuration work without choosing a process exit code.
type ApplyStatus string

const (
	ApplySuccess  ApplyStatus = "success"
	ApplyConflict ApplyStatus = "conflict"
	ApplyError    ApplyStatus = "error"
	ApplyIgnored  ApplyStatus = "ignored"
	ApplyMissing  ApplyStatus = "missing"
	ApplySkipped  ApplyStatus = "skipped"
)

type TargetResult struct {
	manifest.ConfigTarget
	Status  ApplyStatus
	Message string
}

// EntryResult retains all owners, including blocked owners affected by shared files.
type EntryResult struct {
	Path                 string
	Applications         []string
	SelectedApplications []string
	Status               ApplyStatus
	Message              string
	Changed              bool
}

type ApplyResult struct {
	Diagnostics []Diagnostic
	Targets     []TargetResult
	Entries     []EntryResult
}

type targetEntry struct {
	Type string `json:"type"`
}

type (
	commandEnvironmentKey struct{}
	executableContextKey  struct{}
)

// WithExecutable routes chezmoi commands in ctx to an explicit executable.
func WithExecutable(ctx context.Context, executable string) context.Context {
	return context.WithValue(ctx, executableContextKey{}, executable)
}

type applyCommand struct {
	source string
}

func (c applyCommand) run(ctx context.Context, args ...string) ([]byte, string, error) {
	flags := []string{
		"--no-tty", "--no-pager", "--color=false", "--progress=false",
		"--force=false", "--interactive=false", "--dry-run=false", "--refresh-externals=never",
	}
	if c.source != "" {
		flags = append([]string{"--source", c.source}, flags...)
	}
	// No shell; source and destination paths are separate arguments.
	executable := "chezmoi"
	if override, ok := ctx.Value(executableContextKey{}).(string); ok && override != "" {
		executable = override
	}
	// #nosec G204 -- Explicit backend executable; no shell or user-supplied command arguments.
	cmd := exec.CommandContext(ctx, executable, append(flags, args...)...)
	if environment, ok := ctx.Value(commandEnvironmentKey{}).([]string); ok {
		cmd.Env = environment
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, stderr.String(), ctx.Err()
	}
	return stdout, strings.TrimSpace(stderr.String()), err
}

// Apply validates the whole source before computing or writing any destination.
// Targets must be normalized absolute paths. Keep mutation ownership across source
// synchronization, hooks, and this call; concurrent manual writers are unsupported.
// An empty scope does not invoke chezmoi. Blocked owners remain in attribution.
func Apply(
	ctx context.Context,
	source string,
	targets []manifest.ConfigTarget,
	blockedApplications []string,
) (ApplyResult, error) {
	result := ApplyResult{}
	selected := false
	blocked := make(map[string]bool, len(blockedApplications))
	for _, app := range blockedApplications {
		blocked[app] = true
	}
	for _, target := range targets {
		if !filepath.IsAbs(target.Path) || filepath.Clean(target.Path) != target.Path {
			return result, fmt.Errorf("configuration target must be normalized and absolute: %q", target.Path)
		}
		status := ApplySkipped
		if hasEligibleOwner(target, blocked) {
			status = ApplySuccess
			selected = true
		}
		result.Targets = append(result.Targets, TargetResult{ConfigTarget: target, Status: status})
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !selected {
		return result, nil
	}
	validation, err := ValidateSource(ctx, source)
	if err != nil {
		return result, err
	}
	result.Diagnostics = validation.Diagnostics
	if len(result.Diagnostics) != 0 {
		result.failTargets("whole-source validation failed")
		return result, nil
	}
	command := applyCommand{source: source}
	data, message, err := command.run(ctx, "dump-config", "--format=json")
	if err != nil {
		return result.computationFailure(ctx, source, "read effective chezmoi settings", message, err)
	}
	var settings struct {
		DestDir string `json:"destDir"`
		Mode    string `json:"mode"`
	}
	if err = json.Unmarshal(data, &settings); err != nil {
		return result, fmt.Errorf("decode chezmoi settings: %w", err)
	}
	if !filepath.IsAbs(settings.DestDir) || settings.Mode != "file" {
		result.Diagnostics = append(result.Diagnostics, Diagnostic{
			Path: source, Code: "apply-settings",
			Message: "scoped application requires an absolute chezmoi destination and mode=file",
		})
		result.failTargets("unsupported effective chezmoi settings")
		return result, nil
	}
	destination := filepath.Clean(settings.DestDir)
	data, message, err = command.run(ctx, "dump", "--format=json")
	if err != nil {
		return result.computationFailure(ctx, source, "compute whole-source target state", message, err)
	}
	var state map[string]targetEntry
	if err = json.Unmarshal(data, &state); err != nil {
		return result, fmt.Errorf("decode chezmoi target state: %w", err)
	}
	data, message, err = command.run(ctx, "ignored", "--nul-path-separator")
	if err != nil {
		return result.computationFailure(ctx, source, "compute ignored targets", message, err)
	}
	ignored := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
	entries := make(map[string]targetEntry, len(state))
	paths := make([]string, 0, len(state))
	for relative, entry := range state {
		path := filepath.Join(destination, relative)
		if filepath.IsAbs(relative) || !within(destination, path) || path == destination {
			return result, fmt.Errorf("invalid chezmoi target state path: %q", relative)
		}
		entries[path] = entry
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for i := range result.Targets {
		target := &result.Targets[i]
		if target.Status == ApplySkipped {
			continue
		}
		if !within(destination, target.Path) {
			target.Status = ApplyError
			target.Message = "target is outside effective chezmoi destination; " +
				"XDG expansion does not relocate source entries"
			continue
		}
		_, exists := entries[target.Path]
		if !exists && target.Path != destination {
			target.Status = ApplyMissing
			target.Message = "target is absent from the effective source"
			for _, relative := range ignored {
				if relative != "" && within(filepath.Join(destination, relative), target.Path) {
					target.Status = ApplyIgnored
					target.Message = "target is excluded by chezmoi ignore policy"
					break
				}
			}
		}
	}
	selection := make([]TargetResult, 0, len(result.Targets))
	for _, target := range result.Targets {
		if target.Status == ApplySuccess || target.Status == ApplySkipped {
			selection = append(selection, target)
		}
	}
	var fingerprintBuffer [32 * 1024]byte
	for _, path := range paths {
		entry := selectEntry(path, selection, blocked)
		if len(entry.Applications) == 0 {
			continue
		}
		if entry.Status != ApplySkipped {
			entry.Status, entry.Message = checkDestination(path, entries[path].Type)
			if entry.Status == ApplySuccess {
				before, snapshotErr := fingerprintDestination(path, fingerprintBuffer[:])
				if snapshotErr != nil {
					return result, fmt.Errorf("inspect destination before apply %s: %w", path, snapshotErr)
				}
				_, message, err = command.run(ctx,
					"--less-interactive", "--error-on-conflict", "--keep-going",
					"apply", "--recursive=false", "--parent-dirs=false", "--", path,
				)
				if ctx.Err() != nil {
					return result, ctx.Err()
				}
				if err != nil {
					var exit *exec.ExitError
					if !errors.As(err, &exit) {
						return result, fmt.Errorf("execute chezmoi apply: %w", err)
					}
					entry.Status = ApplyError
					relative, _ := filepath.Rel(destination, path)
					if message == "chezmoi: "+relative+": exit status 1" {
						entry.Status = ApplyConflict
					}
					entry.Message = message
					if entry.Message == "" {
						entry.Message = err.Error()
					}
				}
				after, snapshotErr := fingerprintDestination(path, fingerprintBuffer[:])
				if snapshotErr != nil {
					return result, fmt.Errorf("inspect destination after apply %s: %w", path, snapshotErr)
				}
				entry.Changed = before != after
			}
		}
		result.Entries = append(result.Entries, entry)
		if entry.Status == ApplyConflict || entry.Status == ApplyError {
			for i := range result.Targets {
				target := &result.Targets[i]
				if target.Status != ApplySkipped && within(target.Path, path) &&
					(target.Status == ApplySuccess || entry.Status == ApplyError) {
					target.Status, target.Message = entry.Status, entry.Message
				}
			}
		}
	}
	return result, nil
}

// Ignore timestamps and directory children: adopting unchanged files into chezmoi's
// state is not a destination change.
type destinationFingerprint struct {
	exists bool
	mode   fs.FileMode
	link   string
	digest [sha256.Size]byte
}

func fingerprintDestination(path string, buffer []byte) (destinationFingerprint, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return destinationFingerprint{}, nil
	}
	if err != nil {
		return destinationFingerprint{}, err
	}
	state := destinationFingerprint{exists: true, mode: info.Mode()}
	switch {
	case info.Mode().IsRegular():
		file, openErr := os.Open(path)
		if openErr != nil {
			return state, openErr
		}
		digest := sha256.New()
		_, copyErr := io.CopyBuffer(digest, struct{ io.Reader }{file}, buffer)
		closeErr := file.Close()
		if err = errors.Join(copyErr, closeErr); err != nil {
			return state, err
		}
		digest.Sum(state.digest[:0])
	case info.Mode()&os.ModeSymlink != 0:
		state.link, err = os.Readlink(path)
	}
	return state, err
}

func hasEligibleOwner(target manifest.ConfigTarget, blocked map[string]bool) bool {
	for _, owner := range target.Applications {
		if !blocked[owner] {
			return true
		}
	}
	return false
}

func within(parent, path string) bool {
	return path == parent || strings.HasPrefix(path, parent+string(filepath.Separator)) || parent == "/"
}

func appendOwner(owners []string, owner string) []string {
	for _, existing := range owners {
		if existing == owner {
			return owners
		}
	}
	return append(owners, owner)
}

func selectEntry(path string, targets []TargetResult, blocked map[string]bool) EntryResult {
	entry := EntryResult{Path: path, Status: ApplySkipped}
	mostSpecific := -1
	for _, target := range targets {
		if !within(target.Path, path) {
			continue
		}
		for _, owner := range target.Applications {
			entry.Applications = appendOwner(entry.Applications, owner)
			if !blocked[owner] {
				entry.SelectedApplications = appendOwner(entry.SelectedApplications, owner)
			}
		}
		if len(target.Path) > mostSpecific {
			mostSpecific = len(target.Path)
			entry.Status = ApplySkipped
		}
		if len(target.Path) == mostSpecific && hasEligibleOwner(target.ConfigTarget, blocked) {
			entry.Status = ApplySuccess
		}
	}
	if entry.Status == ApplySkipped {
		entry.SelectedApplications = nil
	}
	return entry
}

// Never traverse a destination symlink or replace a different node type. Native
// conflict handling alone can reject a parent but still write through its link.
func checkDestination(path, kind string) (ApplyStatus, string) {
	if kind != "file" && kind != "dir" && kind != "symlink" {
		return ApplyError, "unsupported chezmoi target type: " + kind
	}
	for end := 1; end <= len(path); end++ {
		if end < len(path) && path[end] != filepath.Separator {
			continue
		}
		current := path[:end]
		info, err := os.Lstat(current)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return ApplyError, fmt.Sprintf("inspect destination %s: %v", current, err)
		}
		if err == nil {
			matches := info.IsDir()
			if current == path {
				switch kind {
				case "file":
					matches = info.Mode().IsRegular()
				case "symlink":
					matches = info.Mode()&os.ModeSymlink != 0
				}
			}
			if !matches {
				return ApplyConflict, "preserving incompatible destination type at " + current
			}
		}
	}
	return ApplySuccess, ""
}

func (r *ApplyResult) failTargets(message string) {
	for i := range r.Targets {
		if r.Targets[i].Status != ApplySkipped {
			r.Targets[i].Status, r.Targets[i].Message = ApplyError, message
		}
	}
}

func (r ApplyResult) computationFailure(
	ctx context.Context, source, stage, message string, err error,
) (ApplyResult, error) {
	if ctx.Err() != nil {
		return r, ctx.Err()
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return r, fmt.Errorf("%s: %w", stage, err)
	}
	r.Diagnostics = append(r.Diagnostics, Diagnostic{
		Path: source, Code: "target-computation", Message: stage + ": " + message,
	})
	r.failTargets(stage + " failed")
	return r, nil
}
