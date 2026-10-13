package chezmoi

import (
	"context"
	"errors"
	"fmt"

	"dotm/internal/manifest"
)

// PolicyInputFor reads local declarations for the selected desired closure, not readiness.
// Call it after configuration pre-hooks so their local declaration changes are visible.
func PolicyInputFor(data *manifest.Data, config manifest.Configuration, selectionPath string) (PolicyInput, error) {
	input := PolicyInput{
		Repository: config.Configs.Repository, Configuration: config.Name, SelectionPath: selectionPath,
	}
	filesPath, err := manifest.FilesPath()
	if err != nil {
		return input, err
	}
	input.Files, err = data.Files(config.Apps, filesPath)
	if err != nil {
		return input, err
	}
	resolved, err := data.Resolve(config.Apps)
	if err != nil {
		return input, err
	}
	input.Applications = make([]string, 0, len(resolved.Applications))
	for _, app := range resolved.Applications {
		input.Applications = append(input.Applications, app.ID)
	}
	return input, nil
}

// PolicyRefreshResult retains the candidate and the latest effective diagnostics.
type PolicyRefreshResult struct {
	Policy      Policy
	Diagnostics []manifest.FileDiagnostic
}

// RefreshPolicy composes candidate inspection, publication, and normal effective lint.
// The caller owns mutation; no source acquisition, hooks, or destination apply occurs here.
func RefreshPolicy(ctx context.Context, source string, input PolicyInput) (PolicyRefreshResult, error) {
	result := PolicyRefreshResult{Diagnostics: input.Files.Diagnostics}
	policy, err := CompilePolicy(ctx, source, input)
	result.Policy = policy
	if err != nil {
		return result, err
	}
	candidate, err := CheckPolicy(ctx, source, policy, false)
	result.Diagnostics = append(result.Diagnostics, candidate.Diagnostics...)
	if err = errors.Join(candidate.Err(), err); err != nil {
		return result, err
	}
	if err = PublishPolicy(ctx, source, policy); err != nil {
		return result, err
	}
	published, err := CheckPolicy(ctx, source, policy, true)
	// The post-publication check is authoritative; don't repeat candidate warnings.
	result.Diagnostics = append(input.Files.Diagnostics[:len(input.Files.Diagnostics):len(input.Files.Diagnostics)],
		published.Diagnostics...)
	err = errors.Join(published.Err(), err)
	if err != nil {
		return result, fmt.Errorf("policy was published, but post-publication verification failed: %w", err)
	}
	return result, nil
}
