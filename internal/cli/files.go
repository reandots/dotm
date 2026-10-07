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
	repositoryID := options.Lint.RepositoryID
	updateSource := false
	switch command {
	case "files refresh":
		repositoryID = options.Refresh.RepositoryID
		updateSource = options.Refresh.UpdateSource
	case "files status":
		repositoryID = options.Status.RepositoryID
	}
	filesPath, err := manifest.FilesPath()
	if err != nil {
		return internalError(err)
	}
	files, err := data.Files(config.Apps, filesPath)
	if err != nil {
		return classifyCheckedOrHostError(err)
	}
	if err = printFileDiagnostics(out, files); err != nil {
		return internalError(err)
	}
	if err = files.Err(); err != nil {
		return checkedInputError(err)
	}
	resolved, err := data.Resolve(config.Apps)
	if err != nil {
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
	if err != nil {
		return classifyCheckedOrHostError(err)
	}
	for _, diagnostic := range validation.Diagnostics {
		if _, err = fmt.Fprintf(out, "%s: %s: %s\n", diagnostic.Path, diagnostic.Code, diagnostic.Message); err != nil {
			return internalError(err)
		}
	}
	if len(validation.Diagnostics) != 0 {
		return checkedInputError(errors.New("passive source validation failed; effective templates were not evaluated"))
	}
	if err = chezmoi.ValidateRepository(ctx, source, config.Configs.Repository); err != nil {
		return classifyPolicyError(err)
	}
	applications := make([]string, 0, len(resolved.Applications))
	for _, application := range resolved.Applications {
		applications = append(applications, application.ID)
	}
	input := chezmoi.PolicyInput{
		RepositoryID: repositoryID, Configuration: config.Name,
		SelectionPath: selectionPath, Applications: applications, Files: files,
	}
	policy, err := chezmoi.CompilePolicy(ctx, source, input)
	if err != nil {
		return classifyPolicyError(err)
	}
	path, err := chezmoi.PolicyPath(policy)
	if err != nil {
		return internalError(err)
	}
	if _, err = fmt.Fprintf(out, "Policy path: %s\n", path); err != nil {
		return internalError(err)
	}
	if command == "files lint" {
		model, checkErr := chezmoi.CheckPolicy(ctx, source, policy, true)
		if checkErr != nil {
			return classifyPolicyError(checkErr)
		}
		if err = printFileDiagnostics(out, model); err != nil {
			return internalError(err)
		}
		if err = model.Err(); err != nil {
			return checkedInputError(err)
		}
		_, err = fmt.Fprint(out, "Files policy: valid\n"+
			"Publication: existing policy checked; no publication performed\n")
		if err != nil {
			return internalError(err)
		}
		return nil
	}
	candidate, err := chezmoi.CheckPolicy(ctx, source, policy, false)
	if err != nil {
		return classifyPolicyError(err)
	}
	if err = printFileDiagnostics(out, candidate); err != nil {
		return internalError(err)
	}
	if err = candidate.Err(); err != nil {
		return checkedInputError(err)
	}
	if err = chezmoi.PublishPolicy(ctx, source, policy); err != nil {
		return classifyPolicyError(err)
	}
	published, err := chezmoi.CheckPolicy(ctx, source, policy, true)
	if err != nil {
		return classifyPolicyError(fmt.Errorf(
			"policy was published, but post-publication verification failed: %w", err))
	}
	if err = printFileDiagnostics(out, published); err != nil {
		return internalError(err)
	}
	if err = published.Err(); err != nil {
		return checkedInputError(fmt.Errorf("policy was published, but post-publication verification failed: %w", err))
	}
	if command == "files status" {
		inventory, inventoryErr := chezmoi.Inventory(ctx, source, policy)
		if inventoryErr != nil {
			return classifyPolicyError(inventoryErr)
		}
		warnings := hasFileWarnings(files) || hasFileWarnings(candidate) || hasFileWarnings(published)
		return printFilesStatus(out, inventory, warnings)
	}
	_, err = fmt.Fprint(out, "Files policy: valid\nPublication: refreshed and verified\n")
	if err != nil {
		return internalError(err)
	}
	return nil
}

func printFileDiagnostics(out io.Writer, model manifest.FileModel) error {
	for _, diagnostic := range model.Diagnostics {
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

func classifyCheckedOrHostError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrPermission) {
		return internalError(err)
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) && !errors.Is(pathErr, os.ErrNotExist) {
		return internalError(err)
	}
	return checkedInputError(err)
}
