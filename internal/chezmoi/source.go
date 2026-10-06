// Package chezmoi manages the shared configuration source without running chezmoi
// bootstrap, templates, hooks, or destination operations.
package chezmoi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"dotm/internal/pathutil"
)

// Replacement is a source-wide confirmation, separate from package removal.
type Replacement struct {
	Path          string
	OldRepository string
	Repository    string
}

// ConfirmReplacement must return explicit approval. Nil, false, EOF, and errors
// never authorize replacement. Callers must not reuse removal/cleanup --yes.
type ConfirmReplacement func(context.Context, Replacement) (bool, error)

// SourceResult describes acquisition only, not lint or destination application.
// Backup names a retained old checkout after replacement or a failed handoff.
type SourceResult struct {
	Path      string
	Changed   bool
	Cancelled bool
	Backup    string
}

// SourcePath resolves the standard shared chezmoi checkout. It does not create it.
func SourcePath() (string, error) {
	return pathutil.Target("$XDG_DATA_HOME/chezmoi")
}

// SyncSource acquires or fast-forwards the shared checkout. The lifecycle caller
// must hold dotm mutation ownership throughout, including confirmation. Direct
// manual Git/chezmoi mutations must not run concurrently; rechecks are not locks.
func SyncSource(ctx context.Context, repository string, confirm ConfirmReplacement) (SourceResult, error) {
	path, err := SourcePath()
	if err != nil {
		return SourceResult{}, err
	}
	manager := sourceManager{path: path, rename: os.Rename}
	return manager.sync(ctx, repository, confirm)
}

type sourceManager struct {
	path   string
	rename func(string, string) error
}

func (m sourceManager) sync(
	ctx context.Context, repository string, confirm ConfirmReplacement,
) (result SourceResult, err error) {
	result.Path = m.path
	defer func() {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			result.Cancelled = true
		}
	}()
	if strings.TrimSpace(repository) == "" || strings.ContainsAny(repository, "\x00\r\n") {
		return result, fmt.Errorf("invalid configuration source repository")
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return result, contextErr
	}
	// Fixed recovery evidence must never be overwritten by another invocation.
	if _, markerErr := os.Lstat(m.handoffPath()); !errors.Is(markerErr, os.ErrNotExist) {
		return result, fmt.Errorf("source handoff requires inspection at %s (checkout may be in old): %w",
			m.handoffPath(), handoffError(markerErr))
	}
	info, err := os.Lstat(m.path)
	if errors.Is(err, os.ErrNotExist) {
		return m.acquire(ctx, repository, result)
	}
	if err != nil {
		return result, fmt.Errorf("inspect shared source: %w", err)
	}
	if !info.IsDir() {
		return result, fmt.Errorf("shared source %s is not a directory (symlink roots are unsupported)", m.path)
	}
	old, err := inspectCheckout(ctx, m.path)
	if err != nil {
		return result, err
	}
	if repositoryIdentity(old.repository) != repositoryIdentity(repository) {
		return m.replace(ctx, repository, old, confirm, result)
	}
	return m.update(ctx, old, result)
}

func (m sourceManager) update(ctx context.Context, old checkout, result SourceResult) (SourceResult, error) {
	branch, err := git(ctx, m.path, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return result, fmt.Errorf("cannot fast-forward detached HEAD: %w", err)
	}
	name := strings.TrimPrefix(branch, "refs/heads/")
	remote, err := git(ctx, m.path, "config", "--get", "branch."+name+".remote")
	if err != nil || remote != "origin" {
		return result, fmt.Errorf("source branch must track origin")
	}
	merge, err := git(ctx, m.path, "config", "--get-all", "branch."+name+".merge")
	if err != nil || !strings.HasPrefix(merge, "refs/heads/") || strings.Contains(merge, "\n") {
		return result, fmt.Errorf("source branch must have one upstream branch")
	}
	if _, fetchErr := git(ctx, m.path, "fetch", "--no-tags", "--no-recurse-submodules", "--refmap=",
		"--", "origin", merge); fetchErr != nil {
		return result, fmt.Errorf("fetch source: %w", fetchErr)
	}
	latest, err := git(ctx, m.path, "rev-parse", "--verify", "FETCH_HEAD^{commit}")
	if err != nil {
		return result, err
	}
	current, err := inspectCheckout(ctx, m.path)
	if err != nil {
		return result, err
	}
	if !os.SameFile(old.info, current.info) || current.fingerprint != old.fingerprint {
		return result, fmt.Errorf("source changed while fetching; retry without concurrent manual mutations")
	}
	if _, err := git(ctx, m.path, "merge-base", "--is-ancestor", old.head, latest); err != nil {
		return result, fmt.Errorf("source has unpublished or diverged commits; "+
			"refusing non-fast-forward update: %w", err)
	}
	if latest == old.head {
		return result, nil
	}
	if _, err := git(ctx, m.path, "merge", "--ff-only", "--no-autostash", "--no-squash",
		"--commit", "--no-edit", latest); err != nil {
		return result, fmt.Errorf("fast-forward source: %w", err)
	}
	result.Changed = true
	return result, nil
}

// Repository identity is deliberately conservative: only literal equality and
// canonical local paths/file URLs are equated, never guessed SSH/HTTPS aliases.
func repositoryIdentity(repository string) string {
	path, local := localRepositoryPath(repository)
	if local {
		absolute, err := filepath.Abs(path)
		if err == nil {
			if real, resolveErr := filepath.EvalSymlinks(absolute); resolveErr == nil {
				return real
			}
			return absolute
		}
	}
	return repository
}

func localRepositoryPath(repository string) (string, bool) {
	if strings.HasPrefix(repository, "file:///") {
		return strings.TrimPrefix(repository, "file://"), true
	}
	return repository, !strings.Contains(repository, ":")
}

// ValidateRepository confirms that the existing checkout's origin matches the
// configured repository without contacting the remote.
func ValidateRepository(ctx context.Context, source, repository string) error {
	if strings.TrimSpace(repository) == "" || strings.ContainsAny(repository, "\x00\r\n") {
		return fmt.Errorf("invalid configured source repository")
	}
	origin, err := git(ctx, source, "remote", "get-url", "--all", "origin")
	if err != nil {
		return fmt.Errorf("inspect source origin: %w", err)
	}
	if origin == "" || strings.Contains(origin, "\n") {
		return fmt.Errorf("source must have exactly one effective origin URL")
	}
	if repositoryIdentity(origin) != repositoryIdentity(repository) {
		return fmt.Errorf("source origin %q does not match configured repository %q", origin, repository)
	}
	return nil
}

// Always disable Git hooks and recursive submodules. Git transport/filter helpers
// and trusted local Git configuration are not sandboxed by this backend.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	options := []string{
		"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false",
		"-c", "submodule.recurse=false", "-c", "maintenance.auto=false", "-c", "gc.auto=0",
	}
	//nolint:gosec // Fixed Git executable and internal commands; paths/URLs are individual arguments, not shell input.
	cmd := exec.CommandContext(ctx, "git", append(options, args...)...)
	cmd.Dir = dir
	cmd.Env = gitEnvironment()
	data, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			err = errors.Join(ctx.Err(), err)
		}
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(data)))
	}
	return strings.TrimSuffix(string(data), "\n"), nil
}

func gitEnvironment() []string {
	environment := os.Environ()
	filtered := make([]string, 0, len(environment)+2)
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "GIT_") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered, "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1")
}

func handoffError(err error) error {
	if err != nil {
		return err
	}
	return errors.New("unfinished handoff; recover manually before retrying")
}
