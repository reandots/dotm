package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"dotm/internal/chezmoi"
	"dotm/internal/installed"
	"dotm/internal/manifest"
)

type doctorReport struct {
	out      io.Writer
	failures []error
}

func (r *doctorReport) finding(f installed.DoctorFinding) {
	text := f.Check + ": " + f.Message
	if f.Remedy != "" {
		text += "; remedy: " + f.Remedy
	}
	if f.Severity != 0 {
		r.failures = append(r.failures, &installed.OperationError{
			Severity: f.Severity, Err: errors.New(text),
		})
		return
	}
	label := "ok"
	if f.Blocked {
		label = "blocked"
	}
	fmt.Fprintf(r.out, "%s: %s\n", label, text)
}

func (r *doctorReport) check(name string, err error, remedy string) bool {
	if err == nil {
		r.finding(installed.DoctorFinding{Check: name, Message: "valid"})
		return true
	}
	r.finding(installed.DoctorFinding{
		Check: name, Message: err.Error(), Remedy: remedy, Severity: classifyCheckedOrHostError(err).Severity,
	})
	return false
}

func (r *doctorReport) blocked(name, reason string) {
	r.finding(installed.DoctorFinding{Check: name, Message: reason, Blocked: true})
}

func (r *doctorReport) model(name string, model manifest.FileModel, remedy string) bool {
	valid := true
	for _, d := range model.Diagnostics {
		if d.Severity == manifest.FileError {
			valid = false
			r.finding(installed.DoctorFinding{Check: name, Message: d.String(), Remedy: remedy, Severity: 1})
		} else {
			fmt.Fprintf(r.out, "%s: %s\n", name, d.String())
		}
	}
	return valid
}

func runDoctor(dataDir string, out io.Writer) error {
	ctx := context.Background()
	r := doctorReport{out: out}
	findings, executable := installed.Diagnose(ctx)
	gitReady := false
	for _, finding := range findings {
		r.finding(finding)
		if finding.Check == "git" && finding.Severity == 0 && !finding.Blocked {
			gitReady = true
		}
	}
	if executable != "" {
		ctx = chezmoi.WithExecutable(ctx, executable)
	}

	// Passive source/settings safety is independent of every dotm manifest and backend.
	source, sourceErr := chezmoi.SourcePath()
	passiveReady := false
	integrationReady := false
	if r.check("source path", sourceErr, "Correct HOME/XDG_DATA_HOME in the session") {
		validation, validationErr := chezmoi.ValidateSource(ctx, source)
		passiveReady = r.check("source/settings safety", errors.Join(validation.Err(), validationErr),
			"Use dotm init for a missing source; explicitly correct unsafe source entries or chezmoi settings")
		if passiveReady {
			integration, integrationErr := chezmoi.InspectIntegration(validation.SourceDir)
			integrationReady = r.model("root integration", integration,
				"Use dotm init for missing integration; explicitly correct damaged or unsafe integration")
			if !r.check("root integration inspection", integrationErr, "Correct source access permissions") {
				integrationReady = false
			}
		}
	}
	if !passiveReady {
		r.blocked("root integration", "source/settings safety failed")
	}

	selectionPath, pathErr := manifest.SelectionPath()
	selectionReady := false
	selectionName := ""
	if r.check("selection path", pathErr, "Correct HOME/XDG_CONFIG_HOME in the session") {
		content, readErr := os.ReadFile(selectionPath)
		if readErr == nil {
			selectionName, readErr = manifest.SelectionContents(selectionPath, content)
		}
		selectionReady = r.check("selection", readErr,
			"Use dotm init to prepare selection, or correct the existing config.toml")
		if dataDir == "" {
			dataDir = filepath.Join(filepath.Dir(selectionPath), "data")
		}
	}

	var data *manifest.Data
	if dataDir == "" {
		r.blocked("manifests", "default data directory unavailable because selection path is invalid")
	} else {
		var loadErr error
		data, loadErr = manifest.Load(dataDir)
		r.check("manifests", loadErr, "Use dotm init for absent inputs; correct malformed manifests explicitly")
	}
	var config manifest.Configuration
	if data != nil && selectionReady {
		var exists bool
		config, exists = data.Configs[selectionName]
		if !exists {
			selectionReady = r.check("selected configuration",
				fmt.Errorf("%s: unknown configuration %q", selectionPath, selectionName),
				"Correct selection or configuration definitions")
		}
	}
	if data == nil || !selectionReady {
		r.blocked("declarations", "valid manifests and selection are required")
		r.blocked("effective policy", "valid manifests and selection are required")
		return errors.Join(r.failures...)
	}

	var input chezmoi.PolicyInput
	var model manifest.FileModel
	var filesErr error
	if config.Backend != "" {
		input, filesErr = chezmoi.PolicyInputFor(data, config, selectionPath)
		model = input.Files
	} else {
		var filesPath string
		filesPath, filesErr = manifest.FilesPath()
		if filesErr == nil {
			model, filesErr = data.Files(config.Apps, filesPath)
		}
	}
	declarationsReady := r.model("declarations", model, "Correct catalog or local files.toml declarations")
	if !r.check("declaration inputs", filesErr, "Correct local files.toml or its access permissions") {
		declarationsReady = false
	}
	if config.Backend == "" {
		r.finding(installed.DoctorFinding{
			Check: "configuration source", Severity: 1,
			Message: "selected configuration has no chezmoi source",
			Remedy:  "Use dotm init --repository URL to configure the shared source",
		})
	}
	repositoryReady := false
	if passiveReady && gitReady && config.Backend != "" {
		repositoryReady = r.check("source repository",
			chezmoi.ValidateRepository(ctx, source, config.Repository),
			"Correct source identity explicitly; use dotm init for approved source acquisition or replacement")
	} else {
		r.blocked("source repository", "safe source, operational Git and configured repository are required")
	}
	reasons := make([]string, 0, 6)
	if !passiveReady {
		reasons = append(reasons, "source/settings safety failed")
	}
	if !gitReady {
		reasons = append(reasons, "Git is unavailable or incompatible")
	}
	if executable == "" {
		reasons = append(reasons, "chezmoi is unavailable or incompatible")
	}
	if !declarationsReady {
		reasons = append(reasons, "declarations are invalid")
	}
	if config.Backend == "" {
		reasons = append(reasons, "no configured source")
	}
	if !repositoryReady {
		reasons = append(reasons, "source repository check failed or was blocked")
	}
	if len(reasons) != 0 {
		r.blocked("effective policy", strings.Join(reasons, "; "))
		return errors.Join(r.failures...)
	}
	policyModel, policyErr := chezmoi.DiagnosePolicy(ctx, source, input, integrationReady)
	policyReady := r.model("effective policy", policyModel,
		"Use dotm init for root integration, dotm files refresh for stale policy, or correct native rule conflicts")
	if !r.check("policy execution", policyErr, "Correct source/settings or the reported backend failure") {
		policyReady = false
		r.blocked("effective policy", "backend or input failure prevented complete effective checking")
	}
	if !integrationReady && policyErr == nil {
		r.blocked("policy consumption", "owned root integration is missing or invalid; "+
			"native rules and publication checked independently")
	}
	if policyReady && integrationReady {
		r.finding(installed.DoctorFinding{
			Check: "effective policy", Message: "binding, freshness and native rules valid",
		})
	}
	return errors.Join(r.failures...)
}
