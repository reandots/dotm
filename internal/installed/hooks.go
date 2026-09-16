package installed

import (
	"context"
	"errors"
	"fmt"
	"io"

	"dotm/internal/hooks"
	"dotm/internal/manifest"
)

// Hooks can have arbitrary side effects. Reinspect after a failed phase or a
// successful post phase, but keep package state separate from hook completion.
func runHookPhase(ctx context.Context, desired, scope manifest.Resolved, phase hooks.Phase, current Report, stderr io.Writer) (Report, error) {
	configured, hookErr := hooks.Run(ctx, scope, phase, current.Profile, stderr)
	if !configured || (phase == hooks.Pre && hookErr == nil) {
		return current, hookErr
	}
	if hookErr != nil && phase == hooks.Post {
		hookErr = fmt.Errorf("post-hook failed; package and hook changes were not rolled back: %w", hookErr)
	}
	// Scope controls which hooks run; status must still describe every desired
	// application, including independent missing applications outside update.
	after, inspectErr := Inspect(ctx, desired)
	if inspectErr != nil {
		return Report{}, errors.Join(hookErr, fmt.Errorf("inspect after %s: %w", phase, inspectErr))
	}
	return after, hookErr
}
