package hooks

import "dotm/internal/pathutil"

// resolvePath expands supported hook path variables and resolves relative paths
// against the catalog directory.
func resolvePath(value, catalogDir string) (string, error) {
	return pathutil.Hook(value, catalogDir)
}
