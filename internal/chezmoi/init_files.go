package chezmoi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type integrationParent struct {
	path string
	info os.FileInfo
}

type integrationFile struct {
	path     string
	resolved string
	info     os.FileInfo
	data     []byte
	parents  []integrationParent
}

// Snapshot every existing ancestor, including the resolved identity of directory
// symlinks. File symlinks and nonregular targets are never setup destinations.
func inspectIntegrationFile(path string) (integrationFile, error) {
	file := integrationFile{path: path}
	var err error
	file.info, err = os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return file, err
	}
	if err == nil {
		if !file.info.Mode().IsRegular() {
			return file, fmt.Errorf("%s must be a regular file, not a symlink or special object", path)
		}
		file.data, err = os.ReadFile(path)
		if err != nil {
			return file, err
		}
	}
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		info, statErr := os.Lstat(parent)
		if statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				info, statErr = os.Stat(parent)
				if statErr != nil {
					return file, fmt.Errorf("inspect integration parent %s: %w", parent, statErr)
				}
			}
			if !info.IsDir() {
				return file, fmt.Errorf("integration parent %s is not a directory", parent)
			}
			file.parents = append(file.parents, integrationParent{parent, info})
			if file.resolved == "" {
				real, resolveErr := filepath.EvalSymlinks(parent)
				if resolveErr != nil {
					return file, resolveErr
				}
				relative, relErr := filepath.Rel(parent, path)
				if relErr != nil {
					return file, relErr
				}
				file.resolved = filepath.Join(real, relative)
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return file, statErr
		}
		if parent == filepath.Dir(parent) {
			break
		}
	}
	return file, nil
}

func (file integrationFile) recheck() error {
	current, err := inspectIntegrationFile(file.path)
	if err != nil {
		return err
	}
	changed := file.resolved != current.resolved || !bytes.Equal(file.data, current.data)
	if file.info == nil || current.info == nil {
		changed = changed || (file.info == nil) != (current.info == nil)
	} else {
		changed = changed || !os.SameFile(file.info, current.info) || file.info.Mode() != current.info.Mode() ||
			!file.info.ModTime().Equal(current.info.ModTime())
	}
	for _, parent := range file.parents {
		info, statErr := os.Stat(parent.path)
		if statErr != nil || !os.SameFile(parent.info, info) {
			changed = true
		}
	}
	if changed {
		return fmt.Errorf("integration input %s changed since planning; rerun init", file.path)
	}
	return nil
}

func (file integrationFile) publish(ctx context.Context, content []byte) error {
	if err := file.recheck(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file.path), 0o700); err != nil {
		return err
	}
	staged, err := os.CreateTemp(filepath.Dir(file.path), ".dotm-integration-*")
	if err != nil {
		return err
	}
	defer os.Remove(staged.Name())
	mode := os.FileMode(0o600)
	if file.info != nil {
		mode = file.info.Mode().Perm()
	}
	_, writeErr := staged.Write(content)
	modeErr := staged.Chmod(mode)
	syncErr := staged.Sync()
	closeErr := staged.Close()
	if err = errors.Join(writeErr, modeErr, syncErr, closeErr); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = file.recheck(); err != nil {
		return err
	}
	if file.info == nil {
		return os.Link(staged.Name(), file.path)
	}
	return os.Rename(staged.Name(), file.path)
}
