package installed

import (
	"context"
	"errors"
	"fmt"
	"io"

	"dotm/internal/hooks"
	"dotm/internal/manifest"
)

// Hooks can have arbitrary side effects. Reinspect after a failed phase, but
// keep package state separate from hook completion.
func runHookPhase(
	ctx context.Context,
	desired, scope manifest.Resolved,
	phase hooks.Phase,
	current Report,
	stderr io.Writer,
) (Report, error) {
	configured, hookErr := hooks.Run(ctx, scope, phase, current.Profile, stderr)
	if !configured || hookErr == nil {
		return current, hookErr
	}
	// Scope controls which hooks run; status must still describe every desired
	// application, including independent missing applications outside update.
	after, inspectErr := Inspect(ctx, desired)
	if inspectErr != nil {
		return Report{}, operationError(SeverityInternal,
			errors.Join(hookErr, fmt.Errorf("inspect after %s: %w", phase, inspectErr)))
	}
	return after, hookErr
}
