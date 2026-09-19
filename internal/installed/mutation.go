package installed

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

var errMutationBusy = errors.New("another dotm mutation is running")

type mutationKey struct{}

type mutationLease struct {
	path string
	file *os.File
}

// BeginMutation protects the standard profile and generated source before planning.
// Pass the returned context to backend operations to share ownership within one
// combined invocation, and defer release. Other owners fail immediately.
func BeginMutation(ctx context.Context) (context.Context, func(), error) {
	base, err := stateHome()
	if err != nil {
		return ctx, nil, err
	}
	base, err = canonicalStateHome(base)
	if err != nil {
		return ctx, nil, fmt.Errorf("resolve mutation state directory: %w", err)
	}
	if lease, ok := ctx.Value(mutationKey{}).(*mutationLease); ok && lease.path == base && lease.file != nil {
		return ctx, func() {}, nil
	}
	// A fixed runtime location coordinates even when HOME, TMPDIR, or
	// XDG_RUNTIME_DIR differ. Never create XDG state merely to coordinate a no-op.
	dir := filepath.Join("/tmp", "dotm-"+strconv.Itoa(os.Getuid()))
	if mkdirErr := os.Mkdir(dir, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
		return ctx, nil, fmt.Errorf("create mutation lock directory: %w", mkdirErr)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return ctx, nil, fmt.Errorf("inspect mutation lock directory: %w", err)
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 || !ok || int64(owner.Uid) != int64(os.Getuid()) {
		return ctx, nil, fmt.Errorf("mutation lock directory %s must be private and owned by the current user", dir)
	}
	path := filepath.Join(dir, fmt.Sprintf("%x.lock", sha256.Sum256([]byte(base))))
	// Do not follow substituted symlinks. Descriptors are close-on-exec unless
	// explicitly passed to a native mutation to exclude recovery until it exits.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return ctx, nil, fmt.Errorf("open mutation lock: %w", err)
	}
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		if err != nil {
			return ctx, nil, fmt.Errorf("inspect mutation lock: %w", err)
		}
		return ctx, nil, fmt.Errorf("mutation lock %s is not a regular file", path)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return ctx, nil, fmt.Errorf("%w for %s; retry after it finishes", errMutationBusy, base)
		}
		return ctx, nil, fmt.Errorf("acquire mutation lock: %w", err)
	}
	lease := &mutationLease{path: base, file: file}
	return context.WithValue(ctx, mutationKey{}, lease), func() {
		if lease.file != nil {
			_ = lease.file.Close()
			lease.file = nil
		}
	}, nil
}

// Native mutations retain ownership if their dotm parent is killed, preventing
// recovery from racing a surviving Nix process that may activate a generation.
func retainMutationLease(ctx context.Context, cmd *exec.Cmd) {
	if lease, ok := ctx.Value(mutationKey{}).(*mutationLease); ok && lease.file != nil {
		cmd.ExtraFiles = append(cmd.ExtraFiles, lease.file)
	}
}

// Resolve existing ancestors without creating missing directories. Aliases of
// the same XDG state directory must contend for the same lock, even before its
// last path components exist. A dangling symlink is an error, not absence.
func canonicalStateHome(base string) (string, error) {
	path := base
	var suffix string
	for {
		if _, err := os.Lstat(path); err == nil {
			resolved, resolveErr := filepath.EvalSymlinks(path)
			if resolveErr != nil {
				return "", resolveErr
			}
			return filepath.Join(resolved, suffix), nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		suffix = filepath.Join(filepath.Base(path), suffix)
		path = filepath.Dir(path)
	}
}
