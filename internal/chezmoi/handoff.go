package chezmoi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func (m sourceManager) handoffPath() string {
	return filepath.Join(filepath.Dir(m.path), "."+filepath.Base(m.path)+".dotm-handoff")
}

func (m sourceManager) stage(ctx context.Context, repository string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return "", err
	}
	if err := os.Mkdir(m.handoffPath(), 0o700); err != nil {
		return "", fmt.Errorf("create source handoff: %w", err)
	}
	staged := filepath.Join(m.handoffPath(), "new")
	if _, err := git(ctx, "", "clone", "--no-local", "--no-recurse-submodules", "--template=",
		"--", repository, staged); err != nil {
		m.discardStage()
		return "", fmt.Errorf("clone configuration source: %w", err)
	}
	if _, err := inspectCheckout(ctx, staged); err != nil {
		m.discardStage()
		return "", fmt.Errorf("cloned source is incompatible: %w", err)
	}
	return staged, nil
}

// Only call before moving the old checkout, or after successfully restoring it.
func (m sourceManager) discardStage() {
	_ = os.RemoveAll(m.handoffPath())
}

func (m sourceManager) acquire(
	ctx context.Context, repository string, result SourceResult,
) (SourceResult, error) {
	staged, err := m.stage(ctx, repository)
	if err != nil {
		return result, err
	}
	defer m.discardStage()
	if _, err := os.Lstat(m.path); !errors.Is(err, os.ErrNotExist) {
		return result, fmt.Errorf("source appeared during clone; refusing replacement")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := m.rename(staged, m.path); err != nil {
		return result, fmt.Errorf("activate cloned source: %w", err)
	}
	result.Changed = true
	return result, nil
}

func (m sourceManager) recheck(ctx context.Context, old checkout) error {
	current, err := inspectCheckout(ctx, m.path)
	if err != nil {
		return err
	}
	if !os.SameFile(old.info, current.info) || old.fingerprint != current.fingerprint {
		return fmt.Errorf("source changed while replacement was pending; refusing replacement")
	}
	return nil
}

func (m sourceManager) replace(
	ctx context.Context, repository string, old checkout, confirm ConfirmReplacement, result SourceResult,
) (SourceResult, error) {
	if err := provePublished(ctx, m.path, old.repository); err != nil {
		return result, err
	}
	staged, err := m.stage(ctx, repository)
	if err != nil {
		return result, err
	}
	// Until the first rename, all exits can safely discard only our new clone.
	preserveHandoff := false
	defer func() {
		if !preserveHandoff {
			m.discardStage()
		}
	}()
	if checkErr := m.recheck(ctx, old); checkErr != nil {
		return result, checkErr
	}
	if confirm == nil {
		result.Cancelled = true
		return result, nil
	}
	approved, err := confirm(ctx, Replacement{Path: m.path, OldRepository: old.repository, Repository: repository})
	if err != nil || !approved {
		result.Cancelled = true
		return result, err
	}
	if checkErr := m.recheck(ctx, old); checkErr != nil {
		return result, checkErr
	}
	// Approval may have taken arbitrarily long. Re-establish remote reachability
	// and publication, including any newly created unreachable/reflog objects.
	if publicationErr := provePublished(ctx, m.path, old.repository); publicationErr != nil {
		return result, publicationErr
	}
	if checkErr := m.recheck(ctx, old); checkErr != nil {
		return result, checkErr
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return result, contextErr
	}
	backup, err := os.MkdirTemp(filepath.Dir(m.path), "."+filepath.Base(m.path)+".dotm-backup-")
	if err != nil {
		return result, fmt.Errorf("reserve old-source backup: %w", err)
	}
	// Remove only an empty reservation on failure, never a retained checkout.
	defer func() { _ = os.Remove(backup) }()
	oldPath := filepath.Join(m.handoffPath(), "old")
	if err := m.rename(m.path, oldPath); err != nil {
		return result, fmt.Errorf("preserve old source: %w", err)
	}
	preserveHandoff = true
	result.Backup = oldPath
	if err := m.rename(staged, m.path); err != nil {
		if restoreErr := m.rename(oldPath, m.path); restoreErr != nil {
			return result, fmt.Errorf("source handoff failed; old checkout retained at %s: %w",
				oldPath, errors.Join(err, restoreErr))
		}
		preserveHandoff = false
		result.Backup = ""
		return result, fmt.Errorf("source handoff failed; original restored: %w", err)
	}
	result.Changed = true
	// Rename the journal as a unit: before this atomic step recovery sees the
	// fixed marker; afterwards the new checkout is active and old remains saved.
	if err := m.rename(m.handoffPath(), filepath.Join(backup, "handoff")); err != nil {
		return result, fmt.Errorf("new source active; old checkout retained at %s: %w", oldPath, err)
	}
	result.Backup = filepath.Join(backup, "handoff", "old")
	return result, nil
}
