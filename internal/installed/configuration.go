package installed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"dotm/internal/chezmoi"
	"dotm/internal/hooks"
	"dotm/internal/manifest"
)

// ConfigurationOptions selects the shared source and its separate replacement approval.
// A nil confirmation never authorizes replacing an existing checkout.
type ConfigurationOptions struct {
	Source             *manifest.ConfigurationSource
	ConfirmReplacement chezmoi.ConfirmReplacement
}

type StageStatus string

const (
	StageSuccess StageStatus = "success"
	StageSkipped StageStatus = "skipped"
	StageFailed  StageStatus = "failed"
)

// Err is an own failure; BlockedBy retains immediate dependency/eligibility causes
// without duplicating the original error along every edge in the graph.
type StageResult struct {
	Status    StageStatus
	BlockedBy []string
	Err       error
}

type ConfigurationApplication struct {
	ID        string
	Eligible  bool
	BlockedBy []string
	Pre       StageResult
	Config    StageResult
	Post      StageResult
}

type ConfigurationResult struct {
	Applications []ConfigurationApplication
	Source       chezmoi.SourceResult
	SourceStage  StageResult
	Apply        chezmoi.ApplyResult
	Severity     Severity
}

// Configure executes configuration-only work under the same ownership and pending
// package recovery gate as install/update. It never advances the package source.
func Configure(
	ctx context.Context,
	desired manifest.Resolved,
	stderr io.Writer,
	options ConfigurationOptions,
) (report Report, resultErr error) {
	output := &operationOutput{Writer: stderr}
	defer finishOperation(&report, &resultErr, output)
	ctx, release, err := beginOperation(ctx, output)
	if err != nil {
		return Report{}, err
	}
	defer release()
	before, err := Inspect(ctx, desired)
	if err != nil {
		return Report{}, err
	}
	return configure(ctx, desired, before, output, options)
}

// configure consumes a freshly inspected package result while the caller owns
// mutation. Keep the full target set: removing blocked owners would let broad
// healthy directory targets bypass a failed application's narrower selection.
func configure(
	ctx context.Context,
	desired manifest.Resolved,
	current Report,
	stderr io.Writer,
	options ConfigurationOptions,
) (Report, error) {
	result := ConfigurationResult{SourceStage: StageResult{Status: StageSkipped}}
	if len(desired.Targets) != 0 && (options.Source == nil || options.Source.Backend != "chezmoi" ||
		strings.TrimSpace(options.Source.Repository) == "") {
		result.Severity = SeverityInternal
		current.Configuration = result
		return current, operationError(SeverityInternal, errors.New("configuration targets require a chezmoi source"))
	}
	byID := make(map[string]int, len(desired.Applications))
	if len(current.Applications) != len(desired.Applications) {
		return current, operationError(SeverityInternal, errors.New("incomplete package readiness report"))
	}
	result.Applications = make([]ConfigurationApplication, len(desired.Applications))
	for i, app := range desired.Applications {
		// Inspect preserves the complete resolved application order.
		status := &current.Applications[i]
		if status.ID != app.ID {
			return current, operationError(SeverityInternal, fmt.Errorf("missing package readiness for %q", app.ID))
		}
		byID[app.ID] = i
		result.Applications[i] = ConfigurationApplication{
			ID: app.ID, Eligible: status.Ready, BlockedBy: status.BlockedBy,
			Pre:    StageResult{Status: StageSkipped, BlockedBy: status.BlockedBy},
			Config: StageResult{Status: StageSkipped, BlockedBy: status.BlockedBy},
			Post:   StageResult{Status: StageSkipped, BlockedBy: status.BlockedBy},
		}
	}
	var failures []error
	var blocked []string
	configuredHooks := false
	for i, app := range desired.Applications {
		state := &result.Applications[i]
		if state.Eligible {
			state.Pre.BlockedBy = failedDependencies(app, result.Applications, byID, hooks.PreCfg)
			if len(state.Pre.BlockedBy) == 0 {
				state.Pre.Status = StageSuccess
				ran, hookErr := runConfigurationHook(ctx, desired, app, hooks.PreCfg, current.Profile, stderr)
				configuredHooks = configuredHooks || ran
				if hookErr != nil {
					state.Pre = StageResult{Status: StageFailed, Err: hookErr}
					failures = append(failures, hookErr)
				}
			}
		}
		if state.Pre.Status != StageSuccess {
			blocked = append(blocked, app.ID)
			state.Config.BlockedBy = state.Pre.BlockedBy
			state.Post.BlockedBy = state.Pre.BlockedBy
			if state.Pre.Status == StageFailed {
				state.Config.BlockedBy = []string{app.ID}
				state.Post.BlockedBy = []string{app.ID}
			}
		} else {
			state.Config.Status = StageSuccess
		}
	}

	needsSource := false
	for _, target := range desired.Targets {
		for _, owner := range target.Applications {
			if index, ok := byID[owner]; ok && result.Applications[index].Pre.Status == StageSuccess {
				needsSource = true
			}
		}
	}
	if !needsSource {
		result.Apply.Targets = unappliedTargets(desired.Targets, result.Applications, byID, "no eligible target owners")
	}
	var globalErr error
	if needsSource {
		var sourceErr error
		result.Source, sourceErr = chezmoi.SyncSource(ctx, options.Source.Repository, options.ConfirmReplacement)
		if sourceErr == nil && result.Source.Cancelled {
			sourceErr = errors.New("configuration source replacement was not approved")
		}
		result.SourceStage = StageResult{Status: StageSuccess}
		if sourceErr != nil {
			globalErr = fmt.Errorf("synchronize configuration source: %w", sourceErr)
			result.SourceStage = StageResult{Status: StageFailed, Err: globalErr}
			result.Apply.Targets = unappliedTargets(desired.Targets, result.Applications, byID, globalErr.Error())
		} else {
			result.Apply, globalErr = chezmoi.Apply(ctx, result.Source.Path, desired.Targets, blocked)
			if globalErr != nil {
				globalErr = fmt.Errorf("apply configuration source: %w", globalErr)
			} else if len(result.Apply.Diagnostics) != 0 {
				var diagnostics []error
				for _, diagnostic := range result.Apply.Diagnostics {
					diagnostics = append(diagnostics, fmt.Errorf("%s: %s: %s",
						diagnostic.Path, diagnostic.Code, diagnostic.Message))
				}
				globalErr = errors.Join(diagnostics...)
			}
		}
		if globalErr != nil {
			failures = append(failures, globalErr)
			for i := range result.Applications {
				state := &result.Applications[i]
				if state.Pre.Status == StageSuccess {
					state.Config = StageResult{Status: StageFailed, Err: globalErr}
				}
			}
		} else {
			for _, target := range result.Apply.Targets {
				if target.Status != chezmoi.ApplyConflict && target.Status != chezmoi.ApplyError &&
					target.Status != chezmoi.ApplyMissing {
					continue
				}
				targetErr := fmt.Errorf("configuration target %s: %s: %s", target.Path, target.Status, target.Message)
				failures = append(failures, targetErr)
				for _, owner := range target.Applications {
					if index, ok := byID[owner]; ok && result.Applications[index].Pre.Status == StageSuccess {
						state := &result.Applications[index]
						state.Config.Status = StageFailed
						state.Config.Err = errors.Join(state.Config.Err, targetErr)
					}
				}
			}
		}
	}

	for i, app := range desired.Applications {
		state := &result.Applications[i]
		if state.Pre.Status != StageSuccess {
			continue
		}
		state.Post.BlockedBy = failedDependencies(app, result.Applications, byID, hooks.PostCfg)
		if state.Config.Status != StageSuccess {
			state.Post.BlockedBy = append(state.Post.BlockedBy, app.ID)
		}
		if len(state.Post.BlockedBy) != 0 {
			continue
		}
		state.Post.Status = StageSuccess
		ran, hookErr := runConfigurationHook(ctx, desired, app, hooks.PostCfg, current.Profile, stderr)
		configuredHooks = configuredHooks || ran
		if hookErr != nil {
			state.Post = StageResult{Status: StageFailed, Err: hookErr}
			failures = append(failures, hookErr)
		}
	}
	var resultErr error
	if len(failures) != 0 {
		result.Severity = SeverityConfiguration
		resultErr = operationError(SeverityConfiguration, errors.Join(failures...))
	}
	if configuredHooks {
		after, inspectErr := Inspect(ctx, desired)
		current = after
		if inspectErr != nil {
			result.Severity = SeverityInternal
			resultErr = operationError(SeverityInternal, errors.Join(resultErr,
				fmt.Errorf("inspect after configuration hooks: %w", inspectErr)))
		}
	}
	current.Configuration = result
	return current, resultErr
}

func failedDependencies(
	app manifest.Application,
	states []ConfigurationApplication,
	byID map[string]int,
	phase hooks.Phase,
) []string {
	var blocked []string
	for _, dependency := range app.Dependencies {
		state := &states[byID[dependency]]
		failed := state.Pre.Status != StageSuccess
		if phase == hooks.PostCfg {
			failed = failed || state.Config.Status != StageSuccess || state.Post.Status != StageSuccess
		}
		if failed {
			blocked = append(blocked, dependency)
		}
	}
	return blocked
}

func runConfigurationHook(
	ctx context.Context,
	desired manifest.Resolved,
	app manifest.Application,
	phase hooks.Phase,
	profile string,
	stderr io.Writer,
) (bool, error) {
	// A single-app scope reuses executable/cwd/PATH/output conventions without
	// making the package hook runner responsible for dependency scheduling.
	scope := manifest.Resolved{CatalogDir: desired.CatalogDir, Applications: []manifest.Application{app}}
	return hooks.Run(ctx, scope, phase, profile, stderr)
}

func unappliedTargets(
	targets []manifest.ConfigTarget,
	states []ConfigurationApplication,
	byID map[string]int,
	message string,
) []chezmoi.TargetResult {
	results := make([]chezmoi.TargetResult, 0, len(targets))
	for _, target := range targets {
		status := chezmoi.ApplySkipped
		for _, owner := range target.Applications {
			if index, ok := byID[owner]; ok && states[index].Pre.Status == StageSuccess {
				status = chezmoi.ApplyError
				break
			}
		}
		results = append(results, chezmoi.TargetResult{ConfigTarget: target, Status: status, Message: message})
	}
	return results
}
