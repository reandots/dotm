package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"dotm/internal/chezmoi"
	"dotm/internal/installed"
	"dotm/internal/manifest"
)

func runFiles(command string, options filesCmd, dataDir string, in io.Reader, out, stderr io.Writer) error {
	data, err := manifest.Load(dataDir)
	if err != nil {
		return classifyCheckedOrHostError(err)
	}
	selectionPath, err := manifest.SelectionPath()
	if err != nil {
		return internalError(err)
	}
	config, err := data.Selection(selectionPath)
	if err != nil {
		return classifyCheckedOrHostError(err)
	}
	if config.Configs == nil || config.Configs.Backend != "chezmoi" {
		return checkedInputError(errors.New("selected configuration requires a configured chezmoi source"))
	}
	updateSource := command == "files refresh" && options.Refresh.UpdateSource
	input, err := chezmoi.PolicyInputFor(data, config, selectionPath)
	if err != nil {
		return classifyCheckedOrHostError(err)
	}
	files := input.Files
	if err = printFileDiagnostics(out, files); err != nil {
		return internalError(err)
	}
	if err = files.Err(); err != nil {
		return checkedInputError(err)
	}
	source, err := chezmoi.SourcePath()
	if err != nil {
		return internalError(err)
	}
	ctx := context.Background()
	if command == "files refresh" || command == "files status" {
		var release func()
		ctx, release, err = installed.BeginMutation(ctx)
		if err != nil {
			return internalError(err)
		}
		defer release()
		if err = installed.CheckPendingRecovery(); err != nil {
			return internalError(err)
		}
		if command == "files refresh" && updateSource {
			if _, err = chezmoi.SyncSource(ctx, config.Configs.Repository, confirmReplacement(in, stderr)); err != nil {
				return classifyAcquisitionError(err)
			}
		}
	}
	validation, err := chezmoi.ValidateSource(ctx, source)
	if err = errors.Join(validation.Err(), err); err != nil {
		return classifyCheckedOrHostError(err)
	}
	if err = chezmoi.ValidateRepository(ctx, source, config.Configs.Repository); err != nil {
		return classifyPolicyError(err)
	}
	if command == "files lint" {
		policy, compileErr := chezmoi.CompilePolicy(ctx, source, input)
		if compileErr != nil {
			return classifyPolicyError(compileErr)
		}
		if err = printPolicyPath(out, policy); err != nil {
			return internalError(err)
		}
		model, checkErr := chezmoi.CheckPolicy(ctx, source, policy, true)
		// Keep findings collected before a later backend/template failure.
		if err = printFileDiagnostics(out, model); err != nil {
			return internalError(err)
		}
		if err = errors.Join(model.Err(), checkErr); err != nil {
			return classifyPolicyError(err)
		}
		_, err = fmt.Fprint(out, "Files policy: valid\n"+
			"Publication: existing policy checked; no publication performed\n")
		if err != nil {
			return internalError(err)
		}
		return nil
	}
	refreshed, refreshErr := chezmoi.RefreshPolicy(ctx, source, input)
	if refreshed.Policy.SourceDir != "" {
		if err = printPolicyPath(out, refreshed.Policy); err != nil {
			return internalError(err)
		}
	}
	effective := manifest.FileModel{Diagnostics: refreshed.Diagnostics[len(files.Diagnostics):]}
	if err = printFileDiagnostics(out, effective); err != nil {
		return internalError(err)
	}
	if refreshErr != nil {
		return classifyPolicyError(refreshErr)
	}
	if command == "files status" {
		inventory, inventoryErr := chezmoi.Inventory(ctx, source, refreshed.Policy)
		if inventoryErr != nil {
			return classifyPolicyError(inventoryErr)
		}
		warnings := hasFileWarnings(files) || hasFileWarnings(effective)
		return printFilesStatus(out, inventory, warnings)
	}
	_, err = fmt.Fprint(out, "Files policy: valid\nPublication: refreshed and verified\n")
	if err != nil {
		return internalError(err)
	}
	return nil
}

func printPolicyPath(out io.Writer, policy chezmoi.Policy) error {
	path, err := chezmoi.PolicyPath(policy)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Policy path: %s\n", path)
	return err
}

func printFileDiagnostics(out io.Writer, model manifest.FileModel) error {
	for _, diagnostic := range model.Diagnostics {
		// Errors are owned by the returned error chain; main emits them on stderr.
		if diagnostic.Severity != manifest.FileWarning {
			continue
		}
		if _, err := fmt.Fprintln(out, diagnostic); err != nil {
			return err
		}
	}
	return nil
}

func hasFileWarnings(model manifest.FileModel) bool {
	for _, diagnostic := range model.Diagnostics {
		if diagnostic.Severity == manifest.FileWarning {
			return true
		}
	}
	return false
}

func checkedInputError(err error) error {
	return &installed.OperationError{Severity: installed.SeverityConfiguration, Err: err}
}

func internalError(err error) error {
	return &installed.OperationError{Severity: installed.SeverityInternal, Err: err}
}

func classifyAcquisitionError(err error) error {
	return classifyCheckedOrHostError(err)
}

func classifyPolicyError(err error) error {
	return classifyCheckedOrHostError(err)
}

func classifyCheckedOrHostError(err error) *installed.OperationError {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrPermission) {
		return &installed.OperationError{Severity: installed.SeverityInternal, Err: err}
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) && !errors.Is(pathErr, os.ErrNotExist) {
		return &installed.OperationError{Severity: installed.SeverityInternal, Err: err}
	}
	return &installed.OperationError{Severity: installed.SeverityConfiguration, Err: err}
}
