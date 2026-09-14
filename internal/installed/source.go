package installed

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"dotm/internal/manifest"
)

func managedAttribute(p manifest.Package) string {
	return "dotm_" + hex.EncodeToString([]byte(p.Key()))
}

func prepareSource(ctx context.Context, source, system string, packages []manifest.Package, allowInit bool, stderr io.Writer) error {
	hasLock, err := writeSource(source, system, packages, allowInit)
	if err != nil {
		return err
	}
	if !hasLock {
		cmd := exec.CommandContext(ctx, "nix", "flake", "lock", "path:"+source)
		cmd.Stderr = stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("initialize source lock: %w", err)
		}
	}
	return nil
}

// writeSource validates lock retention before rewriting the generated flake.
// The caller chooses whether to initialize or explicitly advance the lock.
func writeSource(source, system string, packages []manifest.Package, allowInit bool) (bool, error) {
	lockPath := filepath.Join(source, "flake.lock")
	info, err := os.Lstat(lockPath)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("inspect source lock %q: %w", lockPath, err)
	}
	if err == nil && !info.Mode().IsRegular() {
		return false, fmt.Errorf("source lock %q is not a regular file", lockPath)
	}
	if os.IsNotExist(err) && !allowInit {
		return false, fmt.Errorf("source %q has no flake.lock and cannot be initialized while native entries reference it", source)
	}
	hasLock := err == nil
	content := renderSource(system, packages)
	flakePath := filepath.Join(source, "flake.nix")
	current, readErr := os.ReadFile(flakePath)
	if readErr != nil && !os.IsNotExist(readErr) {
		return false, fmt.Errorf("read source flake %q: %w", flakePath, readErr)
	}
	if readErr != nil || string(current) != content {
		if err := os.MkdirAll(source, 0700); err != nil {
			return false, fmt.Errorf("create source directory %q: %w", source, err)
		}
		if err := atomicWrite(flakePath, []byte(content)); err != nil {
			return false, fmt.Errorf("write source flake %q: %w", flakePath, err)
		}
	}
	return hasLock, nil
}

func renderSource(system string, packages []manifest.Package) string {
	var b strings.Builder
	b.WriteString("{\n  inputs.nixpkgs.url = \"github:NixOS/nixpkgs/nixos-unstable\";\n\n  outputs = { self, nixpkgs }: {\n    packages.")
	b.WriteString(system)
	b.WriteString(" = {\n")
	for _, p := range packages {
		b.WriteString("      ")
		b.WriteString(managedAttribute(p))
		b.WriteString(" = nixpkgs.legacyPackages.")
		b.WriteString(system)
		for _, segment := range strings.Split(p.Attr, ".") {
			b.WriteByte('.')
			b.WriteString(strconv.Quote(segment))
		}
		b.WriteString(";\n")
	}
	b.WriteString("    };\n  };\n}\n")
	return b.String()
}

func atomicWrite(path string, content []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".flake.nix-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(content); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
