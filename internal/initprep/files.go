package initprep

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
)

type directory struct {
	path string
	info os.FileInfo
}

type snapshot struct {
	path     string
	content  []byte
	resolved string
	info     os.FileInfo
	parents  []directory
}

func (s snapshot) exists() bool { return s.info != nil }

// Directory symlinks are supported, but their effective directory identities are pinned.
// A dangling link anywhere in the path is an error, never a missing setup input.
func inspect(path string) (snapshot, error) {
	s := snapshot{path: path}
	missing := ""
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				info, err = os.Stat(parent)
			}
			if err != nil {
				return s, fmt.Errorf("inspect directory %s: %w", parent, err)
			}
			if !info.IsDir() {
				return s, fmt.Errorf("unsafe parent %s: expected a directory", parent)
			}
			if s.resolved == "" {
				real, resolveErr := filepath.EvalSymlinks(parent)
				if resolveErr != nil {
					return s, fmt.Errorf("resolve directory %s: %w", parent, resolveErr)
				}
				s.resolved = filepath.Join(real, missing, filepath.Base(path))
			}
			s.parents = append(s.parents, directory{path: parent, info: info})
		} else if !os.IsNotExist(err) {
			return s, fmt.Errorf("inspect directory %s: %w", parent, err)
		}
		missing = filepath.Join(filepath.Base(parent), missing)
		if parent == filepath.Dir(parent) {
			break
		}
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, fmt.Errorf("inspect %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return s, fmt.Errorf("unsafe input %s: expected a regular file, not a symlink or special file", path)
	}
	s.info = info
	s.content, err = os.ReadFile(path)
	if err != nil {
		return s, fmt.Errorf("read %s: %w", path, err)
	}
	return s, nil
}

func checkParents(path string) error {
	_, err := inspect(path)
	return err
}

func (s snapshot) recheck() error {
	current, err := inspect(s.path)
	if err != nil {
		return err
	}
	if s.resolved != current.resolved {
		return fmt.Errorf("input path changed since planning: %s; prepare a new plan", s.path)
	}
	if s.exists() != current.exists() || !bytes.Equal(s.content, current.content) {
		return fmt.Errorf("input changed since planning: %s; prepare a new plan", s.path)
	}
	if s.exists() && (!os.SameFile(s.info, current.info) || s.info.Mode() != current.info.Mode() ||
		!s.info.ModTime().Equal(current.info.ModTime())) {
		return fmt.Errorf("input changed since planning: %s; prepare a new plan", s.path)
	}
	for _, parent := range s.parents {
		info, statErr := os.Stat(parent.path)
		if statErr != nil || !os.SameFile(parent.info, info) {
			return fmt.Errorf("input directory changed since planning: %s; prepare a new plan", parent.path)
		}
	}
	return nil
}

func publish(ctx context.Context, input snapshot, content []byte) error {
	if err := input.recheck(); err != nil {
		return err
	}
	parent := filepath.Dir(input.path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(parent, ".dotm-init-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	mode := os.FileMode(0o600)
	if input.exists() {
		mode = input.info.Mode().Perm()
	}
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := input.recheck(); err != nil {
		return err
	}
	if !input.exists() {
		// Publish without overwriting a file that appeared after the last recheck.
		return os.Link(file.Name(), input.path)
	}
	return os.Rename(file.Name(), input.path)
}
