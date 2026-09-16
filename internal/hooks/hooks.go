package hooks

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"dotm/internal/manifest"
)

type Phase string

const (
	Pre  Phase = "pre_hook"
	Post Phase = "post_hook"
)

// Run executes configured hooks sequentially in resolved application order.
// The boolean reports whether the phase has any configured hooks, including
// hooks that fail path resolution or execution.
func Run(ctx context.Context, desired manifest.Resolved, phase Phase, profile string, stderr io.Writer) (bool, error) {
	configured := false
	var env []string
	for _, app := range desired.Applications {
		value := app.PreHook
		if phase == Post {
			value = app.PostHook
		}
		if value == "" {
			continue
		}
		configured = true
		path, err := resolvePath(value, desired.CatalogDir)
		if err != nil {
			return true, fmt.Errorf("application %q %s path %q: %w", app.ID, phase, value, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return true, fmt.Errorf("application %q %s hook %q: %w", app.ID, phase, path, err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return true, fmt.Errorf("application %q %s hook %q is not a regular executable file", app.ID, phase, path)
		}
		if _, err := fmt.Fprintf(stderr, "application %s %s: %s\n", app.ID, phase, path); err != nil {
			return true, fmt.Errorf("application %q %s hook %q: write log: %w", app.ID, phase, path, err)
		}
		if env == nil {
			env = os.Environ()
			pathEnv := filepath.Join(profile, "bin")
			if inherited := os.Getenv("PATH"); inherited != "" {
				pathEnv += string(os.PathListSeparator) + inherited
			}
			found := false
			for i, entry := range env {
				if strings.HasPrefix(entry, "PATH=") {
					env[i] = "PATH=" + pathEnv
					found = true
					break
				}
			}
			if !found {
				env = append(env, "PATH="+pathEnv)
			}
		}
		cmd := exec.CommandContext(ctx, path)
		cmd.Dir = desired.CatalogDir
		cmd.Env = env
		cmd.Stdout = stderr
		cmd.Stderr = stderr
		if err := cmd.Run(); err != nil {
			return true, fmt.Errorf("application %q %s hook %q: %w", app.ID, phase, path, err)
		}
	}
	return configured, nil
}
