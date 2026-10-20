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
		text += "; to fix: " + f.Remedy
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
	shade := green
	if f.Blocked {
		shade = yellow
	}
	fmt.Fprintf(r.out, "%s: %s\n", colored(r.out, label, shade), text)
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
			"Run dotm init if the source is missing; fix unsafe source files or chezmoi settings manually")
		if passiveReady {
			integration, integrationErr := chezmoi.InspectIntegration(validation.SourceDir)
			integrationReady = r.model("root integration", integration,
				"Run dotm init if integration is missing; fix damaged or unsafe integration manually")
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
		r.check("manifests", loadErr, "Run dotm init for missing files; fix invalid manifests manually")
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
			"Check the source repository URL; run dotm init to download or replace the source with your approval")
	} else {
		r.blocked("source repository", "a safe source, working Git, and a configured repository are required")
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
		"Run dotm init for missing integration, dotm files refresh for outdated rules, or fix chezmoi rule conflicts")
	if !r.check("policy execution", policyErr, "Fix the source, settings, or reported tool error") {
		policyReady = false
		r.blocked("effective policy", "a tool or input error prevented a complete check")
	}
	if !integrationReady && policyErr == nil {
		r.blocked("policy consumption", "dotm integration in the source root is missing or invalid; "+
			"chezmoi rules and saved file rules were checked separately")
	}
	if policyReady && integrationReady {
		r.finding(installed.DoctorFinding{
			Check:   "effective policy",
			Message: "policy matches the current setup; saved rules and chezmoi rules are valid",
		})
	}
	return errors.Join(r.failures...)
}
