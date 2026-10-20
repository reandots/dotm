package installed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

const updateJournalName = "update-recovery.json"

type updateRecovery struct {
	path      string
	data      updateJournal
	finalized bool
}

type updateJournal struct {
	Version    int                       `json:"version"`
	Profile    string                    `json:"profile"`
	Generation int                       `json:"generation"`
	Target     string                    `json:"target"`
	Source     string                    `json:"source,omitempty"`
	Files      map[string]sourceSnapshot `json:"files"`
}

type sourceSnapshot struct {
	Present bool   `json:"present"`
	Data    []byte `json:"data,omitempty"`
}

// beginOperation acquires the shared mutation lease and resolves an interrupted
// update before allowing any later mutation to inspect or modify state.
func beginOperation(ctx context.Context, stderr io.Writer) (context.Context, func(), error) {
	ctx, release, err := BeginMutation(ctx)
	if err != nil {
		return ctx, nil, err
	}
	base, err := stateHome()
	if err == nil {
		journal := filepath.Join(base, "dotm", updateJournalName)
		if _, statErr := os.Lstat(journal); statErr == nil {
			err = recoverJournal(ctx, journal, stderr)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			err = fmt.Errorf("inspect pending update recovery journal: %w", statErr)
		}
	}
	if err != nil {
		release()
		return ctx, nil, err
	}
	return ctx, release, nil
}

// CheckPendingRecovery refuses work while an interrupted package update needs
// explicit recovery. It never starts Nix or attempts recovery itself.
func CheckPendingRecovery() error {
	base, err := stateHome()
	if err != nil {
		return fmt.Errorf("resolve update recovery state: %w", err)
	}
	journal := filepath.Join(base, "dotm", updateJournalName)
	if _, err := os.Lstat(journal); err == nil {
		return errors.New("pending update recovery journal exists; recover it before setup or files refresh")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect pending update recovery journal: %w", err)
	}
	return nil
}

// beginUpdateRecovery durably records the original source files and native
// generation before an update performs its first mutation.
func beginUpdateRecovery(ctx context.Context, profile string, stderr io.Writer) (*updateRecovery, error) {
	base, err := stateHome()
	if err != nil {
		return nil, fmt.Errorf("resolve update recovery state: %w", err)
	}
	base, err = canonicalStateHome(base)
	if err != nil {
		return nil, fmt.Errorf("resolve update recovery state: %w", err)
	}
	profile, err = filepath.Abs(profile)
	if err != nil {
		return nil, fmt.Errorf("resolve update profile: %w", err)
	}
	profile = filepath.Clean(profile)
	h, err := readHistory(profile)
	if err != nil {
		return nil, fmt.Errorf("snapshot update profile: %w", err)
	}
	if h.current <= 0 || len(h.items) == 0 {
		return nil, fmt.Errorf("cannot update without an active native profile generation")
	}
	var target string
	for _, g := range h.items {
		if g.number == h.current {
			target = g.path
			break
		}
	}
	resolvedProfile, err := filepath.EvalSymlinks(filepath.Dir(profile))
	if err != nil {
		return nil, fmt.Errorf("resolve update profile directory: %w", err)
	}
	profile = filepath.Join(resolvedProfile, filepath.Base(profile))
	state, err := readSourceState(base)
	if err != nil {
		return nil, fmt.Errorf("resolve update source: %w", err)
	}
	if state.Path == "" || !validSourcePath(base, state.Path) {
		return nil, fmt.Errorf("cannot snapshot unidentified source: %s", state.Reason)
	}
	source := state.Path
	j := updateJournal{
		Version: 2, Profile: profile, Generation: h.current, Target: target, Source: source,
		Files: make(map[string]sourceSnapshot, 2),
	}
	for _, name := range []string{"flake.nix", "flake.lock"} {
		p := filepath.Join(source, name)
		info, statErr := os.Lstat(p)
		if errors.Is(statErr, os.ErrNotExist) {
			j.Files[name] = sourceSnapshot{}
			continue
		}
		if statErr != nil {
			return nil, fmt.Errorf("snapshot source file %s: %w", name, statErr)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("snapshot source file %s is not a regular file", name)
		}
		b, readErr := os.ReadFile(p)
		if readErr != nil {
			return nil, fmt.Errorf("snapshot source file %s: %w", name, readErr)
		}
		j.Files[name] = sourceSnapshot{Present: true, Data: b}
	}
	journalPath := filepath.Join(base, "dotm", updateJournalName)
	if _, statErr := os.Lstat(journalPath); statErr == nil {
		return nil, fmt.Errorf("pending update recovery journal exists; recover it before another update")
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect update recovery journal: %w", statErr)
	}
	if mkdirErr := durableMkdir(filepath.Dir(journalPath)); mkdirErr != nil {
		return nil, fmt.Errorf("create update recovery state directory: %w", mkdirErr)
	}
	if syncErr := syncDirectory(base); syncErr != nil {
		return nil, fmt.Errorf("sync recovery state parent: %w", syncErr)
	}
	if syncErr := syncDirectory(h.directory); syncErr != nil {
		return nil, fmt.Errorf("sync original profile history: %w", syncErr)
	}
	if validationErr := validateJournal(j, base); validationErr != nil {
		return nil, fmt.Errorf("validate original recovery state: %w", validationErr)
	}
	b, err := json.Marshal(j)
	if err != nil {
		return nil, err
	}
	if err := durableCreate(journalPath, b, 0o600); err != nil {
		return nil, fmt.Errorf("publish update recovery journal: %w", err)
	}
	fmt.Fprintln(stderr, "Saved update recovery snapshot. Package and source state can be restored. "+
		"User data cannot be restored.")
	return &updateRecovery{path: journalPath, data: j}, nil
}

func (r *updateRecovery) commit() error {
	if r == nil {
		return nil
	}
	info, statErr := os.Lstat(r.path)
	if statErr != nil {
		return fmt.Errorf("inspect committed update recovery journal: %w", statErr)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("committed update recovery journal is not a regular file")
	}
	base, err := stateHome()
	if err != nil {
		return fmt.Errorf("sync committed update state: %w", err)
	}
	base, err = canonicalStateHome(base)
	if err != nil {
		return fmt.Errorf("sync committed update state: %w", err)
	}
	source := filepath.Join(base, "dotm", "source")
	if r.data.Version == 2 {
		source = r.data.Source
		if sourceErr := verifyJournalSource(base, r.data); sourceErr != nil {
			return sourceErr
		}
	}
	if syncErr := syncSource(source); syncErr != nil {
		return fmt.Errorf("sync committed source: %w", syncErr)
	}
	h, err := readHistory(r.data.Profile)
	if err != nil {
		return fmt.Errorf("sync committed native profile state: %w", err)
	}
	if err := syncDirectory(h.directory); err != nil {
		return fmt.Errorf("sync committed native profile directory: %w", err)
	}
	// From here the final endpoint is durable. A failed journal deletion may
	// leave recovery pending, but must not start another partial rollback.
	r.finalized = true
	if err := removeDurable(r.path); err != nil {
		return fmt.Errorf("final update state retained; clear recovery journal: %w", err)
	}
	return nil
}

func (r *updateRecovery) rollback(ctx context.Context, stderr io.Writer) error {
	if r == nil {
		return nil
	}
	if r.finalized {
		fmt.Fprintln(stderr, "Kept final package and source state, but could not remove the recovery journal. "+
			"User data was not restored.")
		return nil
	}
	return restoreJournal(ctx, r.path, r.data, stderr)
}

func recoverJournal(ctx context.Context, path string, stderr io.Writer) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect pending update recovery journal: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("pending update recovery journal is not a regular file")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read pending update recovery journal: %w", err)
	}
	var j updateJournal
	if decodeErr := json.Unmarshal(b, &j); decodeErr != nil {
		return fmt.Errorf("corrupt pending update recovery journal: %w", decodeErr)
	}
	base, err := stateHome()
	if err != nil {
		return fmt.Errorf("validate update recovery state: %w", err)
	}
	base, err = canonicalStateHome(base)
	if err != nil {
		return fmt.Errorf("validate update recovery state: %w", err)
	}
	if err := validateJournal(j, base); err != nil {
		return fmt.Errorf("corrupt pending update recovery journal: %w", err)
	}
	fmt.Fprintln(stderr, "Recovering interrupted update: restoring the original native generation and source files. "+
		"User data cannot be restored.")
	return restoreJournal(ctx, path, j, stderr)
}

func validateJournal(j updateJournal, base string) error {
	if (j.Version != 1 && j.Version != 2) || j.Generation <= 0 {
		return fmt.Errorf("unsupported version or invalid native generation")
	}
	if err := verifyJournalSource(base, j); err != nil {
		return err
	}
	if !filepath.IsAbs(j.Profile) || filepath.Clean(j.Profile) != j.Profile {
		return fmt.Errorf("invalid profile path")
	}
	if j.Target == "" || !filepath.IsAbs(j.Target) || filepath.Clean(j.Target) != j.Target {
		return fmt.Errorf("invalid native target identity")
	}
	expectedParent, err := filepath.EvalSymlinks(filepath.Join(base, "nix"))
	if err != nil || j.Profile != filepath.Join(expectedParent, "profile") {
		return fmt.Errorf("recorded profile is not the standard profile for the current state directory")
	}
	expectedLink := filepath.Join(filepath.Dir(j.Profile), fmt.Sprintf("profile-%d-link", j.Generation))
	if j.Target == expectedLink {
		return fmt.Errorf("native target identity must be resolved")
	}
	h, err := readHistory(j.Profile)
	if err != nil {
		return fmt.Errorf("recorded profile history is unavailable: %w", err)
	}
	found := false
	for _, g := range h.items {
		if g.number == j.Generation && g.path == j.Target {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("recorded native target identity is not in profile history")
	}
	resolvedParent, err := filepath.EvalSymlinks(filepath.Dir(j.Profile))
	if err != nil || filepath.Join(resolvedParent, filepath.Base(j.Profile)) != j.Profile {
		return fmt.Errorf("profile path no longer identifies the recorded profile")
	}
	if len(j.Files) != 2 {
		return fmt.Errorf("invalid source snapshot")
	}
	for _, name := range []string{"flake.nix", "flake.lock"} {
		f, ok := j.Files[name]
		if !ok || (!f.Present && len(f.Data) != 0) {
			return fmt.Errorf("invalid snapshot for %s", name)
		}
	}
	return nil
}

// A journal protects one concrete source, never whatever a replaced pointer
// happens to select. Missing files can be restored; redirected ancestors cannot.
func verifyJournalSource(base string, j updateJournal) error {
	source := filepath.Join(base, "dotm", "source")
	if j.Version == 2 {
		source = j.Source
	}
	if !validSourcePath(base, source) {
		return fmt.Errorf("invalid recorded source path")
	}
	resolved, err := canonicalStateHome(source)
	if err != nil || resolved != source {
		return fmt.Errorf("recorded source path no longer identifies its original location")
	}
	if j.Version == 2 {
		state, readErr := readSourceState(base)
		if readErr != nil {
			return readErr
		}
		if state.Path != source {
			return fmt.Errorf("current source identity changed; recovery snapshot retained")
		}
	}
	return nil
}

func restoreJournal(ctx context.Context, path string, j updateJournal, stderr io.Writer) error {
	base, err := stateHome()
	if err != nil {
		return fmt.Errorf("restore source files: %w", err)
	}
	base, err = canonicalStateHome(base)
	if err != nil {
		return fmt.Errorf("restore source files: %w", err)
	}
	if validationErr := validateJournal(j, base); validationErr != nil {
		return fmt.Errorf("validate recovery snapshot before restoration: %w", validationErr)
	}
	h, err := readHistory(j.Profile)
	if err != nil {
		return fmt.Errorf("read profile before restoration: %w", err)
	}
	fmt.Fprintln(stderr, "Update recovery: restoring native package generation and source; user data is not restored.")
	if h.current != j.Generation {
		args := []string{"profile", "rollback", "--profile", j.Profile, "--to", fmt.Sprint(j.Generation)}
		cmd := exec.CommandContext(ctx, "nix", args...)
		retainMutationLease(ctx, cmd)
		cmd.Stdout, cmd.Stderr = stderr, stderr
		if rollbackErr := cmd.Run(); rollbackErr != nil {
			return fmt.Errorf("restore native generation %d: %w (recovery snapshot retained)",
				j.Generation, rollbackErr)
		}
	}
	h, err = readHistory(j.Profile)
	if err != nil {
		return fmt.Errorf("verify restored native package generation: %w (recovery snapshot retained)", err)
	}
	if h.current != j.Generation {
		return fmt.Errorf("restored native generation is %d, expected %d (recovery snapshot retained)",
			h.current, j.Generation)
	}
	var target string
	for _, g := range h.items {
		if g.number == j.Generation {
			target = g.path
			break
		}
	}
	if target != j.Target {
		return fmt.Errorf("restored native target differs from snapshot (recovery snapshot retained)")
	}
	source := filepath.Join(base, "dotm", "source")
	if j.Version == 2 {
		source = j.Source
	}
	for _, name := range []string{"flake.nix", "flake.lock"} {
		snap := j.Files[name]
		p := filepath.Join(source, name)
		info, statErr := os.Lstat(p)
		if statErr == nil && !info.Mode().IsRegular() {
			return fmt.Errorf("restore source %s: path is not a regular file (recovery snapshot retained)", name)
		}
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("inspect source file %s before restore: %w (recovery snapshot retained)", name, statErr)
		}
		if snap.Present {
			if err := durableMkdir(filepath.Dir(p)); err != nil {
				return fmt.Errorf("restore source file %s: %w (recovery snapshot retained)", name, err)
			}
			if err := durableReplace(p, snap.Data, 0o600); err != nil {
				return fmt.Errorf("restore source file %s: %w (recovery snapshot retained)", name, err)
			}
		} else {
			if err := removeDurable(p); err != nil {
				return fmt.Errorf("restore source file %s absence: %w (recovery snapshot retained)", name, err)
			}
		}
	}
	for _, name := range []string{"flake.nix", "flake.lock"} {
		p := filepath.Join(source, name)
		snap := j.Files[name]
		info, statErr := os.Lstat(p)
		if !snap.Present {
			if !errors.Is(statErr, os.ErrNotExist) {
				return fmt.Errorf("verify restored source file %s absence failed (recovery snapshot retained)", name)
			}
			continue
		}
		if statErr != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("verify restored source file %s type failed (recovery snapshot retained)", name)
		}
		b, readErr := os.ReadFile(p)
		if readErr != nil || !bytes.Equal(b, snap.Data) {
			return fmt.Errorf("verify restored source file %s content failed (recovery snapshot retained)", name)
		}
	}
	if err := syncDirectory(h.directory); err != nil {
		return fmt.Errorf("sync restored native profile directory: %w (recovery snapshot retained)", err)
	}
	if err := removeDurable(path); err != nil {
		return fmt.Errorf("clear restored update recovery journal: %w", err)
	}
	fmt.Fprintln(stderr, "Update recovery completed.")
	return nil
}

func durableMkdir(path string) error {
	path = filepath.Clean(path)
	if info, err := os.Lstat(path); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory", path)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(path)
	if parent != path {
		if err := durableMkdir(parent); err != nil {
			return err
		}
	}
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func durableCreate(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".update-recovery-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(name, path); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	return nil
}

func durableReplace(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(filepath.Dir(path)), ".update-restore-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	if filepath.Dir(name) != filepath.Dir(path) {
		return syncDirectory(filepath.Dir(name))
	}
	return nil
}

func removeDurable(path string) error {
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func syncDirectory(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return err
	}
	return nil
}
