package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"dotm/internal/chezmoi"
	"dotm/internal/initprep"
	"dotm/internal/installed"
	"dotm/internal/manifest"
)

func runInit(options initCmd, dataDir string, in io.Reader, out, stderr io.Writer) (resultErr error) {
	stage := "mutation ownership"
	defer func() {
		if resultErr != nil {
			resultErr = fmt.Errorf("init failed at %s; completed effects above are retained: %w", stage, resultErr)
		}
	}()
	ctx, release, err := installed.BeginMutation(context.Background())
	if err != nil {
		return err
	}
	defer release()
	stage = "pending recovery check"
	if err = installed.CheckPendingRecovery(); err != nil {
		return err
	}
	stage = "input preparation"
	plan, err := initprep.Plan(ctx, initprep.Options{
		DataDir: dataDir, Configuration: options.Configuration, Repository: options.Repository,
	})
	if err != nil {
		return err
	}
	prepared, err := plan.Apply(ctx, confirmInit(in, out))
	for _, effect := range prepared.Completed {
		action := "changed"
		if effect.Created {
			action = "created"
		}
		fmt.Fprintf(out, "Dotm file %s: %s\n", action, effect.Path)
	}
	if err != nil {
		return err
	}
	stage = "chezmoi backend"
	executable, err := installed.FindChezmoi(ctx)
	if err != nil {
		return err
	}
	var backend installed.BootstrapResult
	if executable == "" {
		stage = "chezmoi application selection"
		applicationPlan, planErr := initprep.PlanChezmoi(ctx, dataDir, plan.Configuration)
		if planErr != nil {
			return planErr
		}
		applicationFiles, applyErr := applicationPlan.Apply(ctx, nil)
		for _, effect := range applicationFiles.Completed {
			fmt.Fprintf(out, "Dotm file changed: %s\n", effect.Path)
		}
		if applyErr != nil {
			return applyErr
		}
		selectionPath, selectionErr := manifest.SelectionPath()
		if selectionErr != nil {
			return selectionErr
		}
		sharedDir := dataDir
		if sharedDir == "" {
			sharedDir = filepath.Join(filepath.Dir(selectionPath), "data")
		}
		data, loadErr := manifest.Load(sharedDir)
		if loadErr != nil {
			return loadErr
		}
		desired, resolveErr := data.Resolve([]string{"chezmoi"})
		if resolveErr != nil {
			return resolveErr
		}
		stage = "chezmoi application installation"
		backend, err = installed.BootstrapChezmoi(ctx, desired, stderr)
		executable = backend.Executable
	}
	for _, effect := range backend.Effects {
		fmt.Fprintf(out, "Backend %s %s: %s\n", effect.Resource, effect.Action, effect.Path)
	}
	if err != nil {
		return err
	}
	ctx = chezmoi.WithExecutable(ctx, executable)
	if verboseOutput(out) {
		fmt.Fprintf(out, "Chezmoi executable: %s\n", executable)
	}
	stage = "source acquisition"
	source, err := chezmoi.SetupSource(ctx, plan.Repository, confirmReplacement(in, out))
	if source.Changed {
		fmt.Fprintf(out, "Checkout acquired: %s\n", source.Path)
	}
	if source.Backup != "" {
		fmt.Fprintf(out, "Retained checkout backup: %s\n", source.Backup)
	}
	if err != nil {
		return err
	}
	stage = "settings and root integration"
	integration, err := chezmoi.PrepareIntegration(ctx, source.Path)
	sourceEdited := false
	for _, effect := range integration.Effects {
		fmt.Fprintf(out, "Integration file %s: %s\n", effect.Action, effect.Path)
		if filepath.Base(effect.Path) == ".chezmoiignore" {
			sourceEdited = true
		}
	}
	// Source edits remain user-owned even if a later stage fails.
	if sourceEdited {
		fmt.Fprintln(out, colored(out,
			"Action required: commit reviewed checkout changes before synchronization.", yellow))
		fmt.Fprintf(out, "Review source edits at %s, including generated .chezmoiignore changes, "+
			"and commit intended source changes yourself before install, update, update cfg, "+
			"or files refresh --update-source; these operations require a clean checkout. "+
			"Move unrelated data outside the checkout. No source changes are committed automatically.\n", source.Path)
	}
	if err != nil {
		return err
	}
	stage = "initial policy"
	selectionPath, err := manifest.SelectionPath()
	if err != nil {
		return err
	}
	if dataDir == "" {
		dataDir = filepath.Join(filepath.Dir(selectionPath), "data")
	}
	data, err := manifest.Load(dataDir)
	if err != nil {
		return err
	}
	config, err := data.Selection(selectionPath)
	if err != nil {
		return err
	}
	input, err := chezmoi.PolicyInputFor(data, config, selectionPath)
	if err != nil {
		return err
	}
	refreshed, err := chezmoi.RefreshPolicy(ctx, source.Path, input)
	if refreshed.Published {
		if printErr := printPolicyPath(out, refreshed.Policy); printErr != nil {
			return errors.Join(err, printErr)
		}
	}
	if printErr := printFileDiagnostics(out, manifest.FileModel{Diagnostics: refreshed.Diagnostics}); printErr != nil {
		return errors.Join(err, printErr)
	}
	if err != nil {
		return err
	}
	stage = "normal chezmoi discovery"
	if err = chezmoi.VerifyDiscovery(ctx, source.Path, refreshed.Policy); err != nil {
		return err
	}
	printOutcome(out, "OK", "Init complete; only missing chezmoi and its dependencies may have been installed; "+
		"no configuration hooks or destination configurations were applied.", green)
	return nil
}

func confirmInit(in io.Reader, out io.Writer) initprep.Confirm {
	reader, ok := in.(*bufio.Reader)
	if !ok {
		reader = bufio.NewReader(in)
	}
	return func(ctx context.Context, conflict initprep.Conflict) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if _, err := fmt.Fprintf(out, "Change %s in %s?\nOld value: %q\nNew value: %q\n"+
			"Type yes to approve: ", conflict.Field, conflict.Path, conflict.Before, conflict.After); err != nil {
			return false, err
		}
		answer, err := reader.ReadString('\n')
		if err != nil {
			return false, err
		}
		if err = ctx.Err(); err != nil {
			return false, err
		}
		return strings.TrimSpace(answer) == "yes", nil
	}
}
