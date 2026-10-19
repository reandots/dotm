package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"dotm/internal/chezmoi"
	"dotm/internal/installed"
	"dotm/internal/manifest"
)

// ExitCode returns the maximum severity, including all errors of combined operations.
// Codes are 0 success, 1 configuration failure, 2 installation failure, 3 dotm failure.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	// Do not use errors.As: the outer typed boundary overrides causes; joined operations need their maximum.
	//nolint:errorlint // Intentional exact type check before manually traversing wrappers and joins.
	if outcome, ok := err.(*installed.OperationError); ok {
		return int(outcome.Severity)
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		code := 0
		for _, cause := range joined.Unwrap() {
			code = max(code, ExitCode(cause))
		}
		return code
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return ExitCode(wrapped.Unwrap())
	}
	return 3
}

type checkedWriter struct {
	io.Writer
	err     error
	verbose bool
}

func (w *checkedWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if w.err == nil {
		w.err = err
	}
	return n, err
}

func lintSource(source string, out io.Writer) error {
	if source == "" {
		var err error
		source, err = chezmoi.SourcePath()
		if err != nil {
			return err
		}
	}
	result, err := chezmoi.ValidateSource(context.Background(), source)
	if err != nil {
		return errors.Join(result.Err(), err)
	}
	fmt.Fprintf(out, "Configuration source: %s\n", result.SourceDir)
	if len(result.Diagnostics) == 0 {
		printOutcome(out, "OK", "Configuration policy: valid (templates are not evaluated)", green)
		return nil
	}
	return &installed.OperationError{Severity: installed.SeverityConfiguration, Err: result.Err()}
}

func confirmReplacement(in io.Reader, out io.Writer) chezmoi.ConfirmReplacement {
	reader, ok := in.(*bufio.Reader)
	if !ok {
		reader = bufio.NewReader(in)
	}
	return func(ctx context.Context, replacement chezmoi.Replacement) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if _, err := fmt.Fprintf(out,
			"Replace configuration checkout %s?\nOld repository: %s\nNew repository: %s\n"+
				"Delete the old checkout and replace it? Type yes to approve: ",
			replacement.Path, replacement.OldRepository, replacement.Repository); err != nil {
			return false, err
		}
		answer, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return false, nil
			}
			return false, err
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		return strings.TrimSpace(answer) == "yes", nil
	}
}

func printConfiguration(out io.Writer, result installed.ConfigurationResult) {
	if len(result.Applications) == 0 && result.SourceStage.Status == "" {
		return
	}
	fmt.Fprintf(out, "Configuration outcome: %s\n", severityName(result.Severity))
	fmt.Fprintf(out, "Source stage: %s", result.SourceStage.Status)
	if result.Source.Path != "" {
		fmt.Fprintf(out, " (%s; changed=%t)", result.Source.Path, result.Source.Changed)
	}
	fmt.Fprintln(out)
	if result.Source.Backup != "" {
		fmt.Fprintf(out, "Retained source backup: %s\n", result.Source.Backup)
	}
	fmt.Fprintf(out, "Files policy stage: %s\n", result.PolicyStage.Status)
	// Errors retain their wrapped return path; only warnings belong in reports.
	for _, diagnostic := range result.Policy.Diagnostics {
		if diagnostic.Severity == manifest.FileWarning {
			fmt.Fprintln(out, diagnostic)
		}
	}
	for _, app := range result.Applications {
		fmt.Fprintf(out, "  %s: ", app.ID)
		if !app.Eligible {
			fmt.Fprintf(out, "expected skip; package requirements not ready: %s\n", list(app.BlockedBy))
			continue
		}
		fmt.Fprintln(out)
		for _, stage := range []struct {
			name   string
			result installed.StageResult
		}{{"pre_cfg_hook", app.Pre}, {"configs", app.Config}, {"post_cfg_hook", app.Post}} {
			fmt.Fprintf(out, "    %s: %s", stage.name, stage.result.Status)
			if len(stage.result.BlockedBy) > 0 {
				fmt.Fprintf(out, "; blocked by %s", list(stage.result.BlockedBy))
			}
			fmt.Fprintln(out)
		}
	}
	for _, target := range result.Apply.Targets {
		fmt.Fprintf(out, "  Target %s [%s; owners: %s]",
			target.Path, target.Status, list(target.Applications))
		if target.Message != "" && !failedApplyStatus(target.Status) &&
			result.SourceStage.Err == nil && result.PolicyStage.Err == nil && len(result.Apply.Diagnostics) == 0 {
			fmt.Fprintf(out, ": %s", target.Message)
		}
		fmt.Fprintln(out)
	}
	for _, entry := range result.Apply.Entries {
		fmt.Fprintf(out, "    Path %s [%s; owners: %s; selected: %s]",
			entry.Path, entry.Status, list(entry.Applications), list(entry.SelectedApplications))
		if entry.Message != "" && !failedApplyStatus(entry.Status) {
			fmt.Fprintf(out, ": %s", entry.Message)
		}
		fmt.Fprintln(out)
	}
}

func printConfigurationMode(out io.Writer, result installed.ConfigurationResult, verbose bool) {
	if verbose {
		printConfiguration(out, result)
		return
	}
	if len(result.Applications) == 0 && result.SourceStage.Status == "" {
		return
	}
	shade, label := green, "OK"
	if result.Severity != installed.SeveritySuccess {
		shade, label = red, "Failed"
	}
	outcome := "completed"
	if result.Severity != installed.SeveritySuccess {
		outcome = severityName(result.Severity)
	}
	printOutcome(out, label, "Configuration: "+outcome, shade)
	if result.Source.Changed {
		fmt.Fprintf(out, "  Source synchronized: %s\n", result.Source.Path)
	}
	if result.Source.Backup != "" {
		fmt.Fprintf(out, "  Retained source backup: %s\n", result.Source.Backup)
	}
	for _, diagnostic := range result.Policy.Diagnostics {
		if diagnostic.Severity == manifest.FileWarning {
			fmt.Fprintln(out, colored(out, diagnostic.String(), yellow))
		}
	}
	if result.SourceStage.Status == installed.StageFailed {
		fmt.Fprintf(out, "  Source synchronization failed: %s\n", result.Source.Path)
		fmt.Fprintln(out, "  Configurations were not applied; "+
			"completed package and pre-configuration hook effects remain.")
		return
	}
	if result.PolicyStage.Status == installed.StageFailed {
		fmt.Fprintln(out, "  File-policy checks failed; configurations were not applied.")
		return
	}
	for _, app := range result.Applications {
		if !app.Eligible {
			fmt.Fprintf(out, "  %s %s: package requirements not ready; blocked by %s\n",
				colored(out, "Skipped", yellow), app.ID, list(app.BlockedBy))
			continue
		}
		for _, stage := range []struct {
			name   string
			result installed.StageResult
		}{{"pre-config hook", app.Pre}, {"configs", app.Config}, {"post-config hook", app.Post}} {
			if stage.result.Status == "" || stage.result.Status == installed.StageSuccess {
				continue
			}
			shade := yellow
			if stage.result.Status == installed.StageFailed {
				shade = red
			}
			fmt.Fprintf(out, "  %s: %s %s", app.ID, stage.name, colored(out, string(stage.result.Status), shade))
			if len(stage.result.BlockedBy) != 0 {
				fmt.Fprintf(out, "; blocked by %s", list(stage.result.BlockedBy))
			}
			fmt.Fprintln(out)
		}
	}
	for _, entry := range result.Apply.Entries {
		if entry.Changed {
			fmt.Fprintf(out, "  %s %s (owners: %s)\n", colored(out, "Changed", green),
				entry.Path, list(entry.SelectedApplications))
		}
	}
	for _, target := range result.Apply.Targets {
		if target.Status == chezmoi.ApplySuccess || target.Status == chezmoi.ApplyIgnored {
			continue
		}
		shade := yellow
		if failedApplyStatus(target.Status) {
			shade = red
		}
		fmt.Fprintf(out, "  %s %s (owners: %s)\n", colored(out, string(target.Status), shade),
			target.Path, list(target.Applications))
	}
}

func failedApplyStatus(status chezmoi.ApplyStatus) bool {
	return status == chezmoi.ApplyConflict || status == chezmoi.ApplyError || status == chezmoi.ApplyMissing
}

func severityName(severity installed.Severity) string {
	switch severity {
	case installed.SeveritySuccess:
		return "success"
	case installed.SeverityConfiguration:
		return "configuration failure (partial work may have completed)"
	case installed.SeverityInstallation:
		return "installation failure"
	default:
		return "dotm failure"
	}
}

// PrintError emits the returned failure once, preserving its original causes.
func PrintError(out io.Writer, err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(out, "%s %v\n", colored(out, "dotm:", red), err)
}
