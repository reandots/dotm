package chezmoi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"dotm/internal/manifest"
)

// InspectIntegration reports whether the owned policy integration is present
// and intact at an effective source root from successful passive validation.
func InspectIntegration(sourceRoot string) (manifest.FileModel, error) {
	result := manifest.FileModel{}
	ignorePath := filepath.Join(sourceRoot, ".chezmoiignore")
	contents, err := os.ReadFile(ignorePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return result, fmt.Errorf("inspect owned policy integration: %w", err)
	}
	if errors.Is(err, os.ErrNotExist) {
		contents = nil
	}
	portable, _ := integrationTemplates()
	_, changed, integrationErr := integrateIgnore(contents, ownedIntegration(portable), ignorePath)
	if integrationErr != nil {
		result.Diagnostics = append(result.Diagnostics, manifest.FileDiagnostic{
			Severity: manifest.FileError, Path: ignorePath,
			Message: fmt.Sprintf("owned policy integration is damaged: %v; "+
				"run dotm init or correct it manually", integrationErr),
		})
	} else if changed {
		result.Diagnostics = append(result.Diagnostics, manifest.FileDiagnostic{
			Severity: manifest.FileError, Path: ignorePath,
			Message: "owned policy integration is missing or outdated; run dotm init",
		})
	}
	return result, nil
}

// DiagnosePolicy checks effective rules and the published policy binding.
// Call after passive source validation and usable manifest/input checks.
func DiagnosePolicy(ctx context.Context, source string, input PolicyInput,
	integrationValid bool,
) (manifest.FileModel, error) {
	policy, err := CompilePolicy(ctx, source, input)
	if err != nil {
		return manifest.FileModel{}, err
	}
	return checkPolicy(ctx, source, policy, true, integrationValid)
}
