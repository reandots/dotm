package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"dotm/internal/installed"
	"dotm/internal/manifest"

	"github.com/spf13/cobra"
)

type cli struct {
	DataDir         string
	Verbose         bool
	InstallMissing  bool
	UpdateInstalled bool
	RemoveExtra     bool
	Yes             bool
	Init            initCmd
	Clean           cleanCmd
	Cfg             cfgCmd
	Files           filesCmd
}

type cleanCmd struct {
	Keep   int
	GC     bool
	DryRun bool
}

type cfgCmd struct {
	Lint struct{ Source string }
}

type filesCmd struct {
	Refresh struct{ UpdateSource bool }
}

type initCmd struct {
	Configuration string
	Repository    string
}

// Run executes a CLI invocation using the supplied arguments and streams.
// It returns errors to the caller without exiting the process.
func Run(args []string, in io.Reader, out, stderr io.Writer) (resultErr error) {
	output, diagnostics := &checkedWriter{Writer: out}, &checkedWriter{Writer: stderr}
	out, stderr = output, diagnostics
	defer func() {
		if output.err != nil || diagnostics.err != nil {
			resultErr = &installed.OperationError{
				Severity: installed.SeverityInternal,
				Err:      errors.Join(resultErr, output.err, diagnostics.err),
			}
		}
	}()
	in = bufio.NewReader(in)
	var app cli
	command := ""
	root := app.command(&command)
	root.SetArgs(args)
	root.SetIn(in)
	root.SetOut(out)
	root.SetErr(stderr)
	root.InitDefaultCompletionCmd()
	for _, cmd := range root.Commands() {
		if cmd.Name() == "completion" {
			cmd.Short = "Generate a shell completion script"
			// A runnable parent makes Cobra validate unknown shell arguments.
			cmd.RunE = func(cmd *cobra.Command, _ []string) error { return cmd.Help() }
			break
		}
	}
	if err := root.Execute(); err != nil {
		return err
	}
	// Help and Cobra's completion commands finish without entering application execution.
	if command == "" {
		return nil
	}
	output.verbose = app.Verbose
	diagnostics.verbose = app.Verbose
	// Parsing and dispatch have no manifest or backend side effects.
	hasOperations := app.InstallMissing || app.UpdateInstalled || app.RemoveExtra
	if command == "operations" && !hasOperations {
		return fmt.Errorf("expected a command or at least one of -i, -u, -r")
	}
	if command != "operations" && hasOperations {
		return fmt.Errorf("operation flags cannot be combined with a subcommand")
	}
	if app.Yes && !app.RemoveExtra && command != "remove-extras" && command != "clean" {
		return fmt.Errorf("--yes requires -r, remove-extras, or clean")
	}
	if command == "clean" {
		if app.Clean.Keep < 0 {
			return fmt.Errorf("--keep must be nonnegative")
		}
		return installed.Clean(context.Background(), installed.CleanOptions{
			Keep:   app.Clean.Keep,
			GC:     app.Clean.GC,
			DryRun: app.Clean.DryRun,
			Yes:    app.Yes,
		}, in, out, stderr)
	}
	if command == "cfg lint" {
		return lintSource(app.Cfg.Lint.Source, out)
	}
	if command == "init" {
		return runInit(app.Init, app.DataDir, in, out, stderr)
	}
	if command == "doctor" {
		return runDoctor(app.DataDir, out)
	}
	if app.DataDir == "" {
		selectionPath, pathErr := manifest.SelectionPath()
		if pathErr != nil {
			return pathErr
		}
		app.DataDir = filepath.Join(filepath.Dir(selectionPath), "data")
	}
	if command == "files lint" || command == "files refresh" || command == "files status" {
		return runFiles(command, app.Files, app.DataDir, in, out, stderr)
	}

	data, err := manifest.Load(app.DataDir)
	if err != nil {
		return err
	}
	switch command {
	case "configs":
		names := make([]string, 0, len(data.Configs))
		for name := range data.Configs {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Fprintf(out, "%s: %s\n", name, list(data.Configs[name].Apps))
		}
	case "apps":
		for _, id := range data.ApplicationIDs() {
			app := data.Apps[id]
			packages := make([]string, 0, len(app.Packages))
			for _, p := range app.Packages {
				packages = append(packages, p.Key())
			}
			fmt.Fprintf(out, "%s\n  dependencies: %s\n  nix: %s\n", id, list(app.Dependencies), list(packages))
			if app.PreHook != "" {
				fmt.Fprintf(out, "  pre_hook: %s\n", app.PreHook)
			}
			fmt.Fprintf(out, "  configs: %s\n", list(app.Configs))
			if app.PreCfgHook != "" {
				fmt.Fprintf(out, "  pre_cfg_hook: %s\n", app.PreCfgHook)
			}
			if app.PostCfgHook != "" {
				fmt.Fprintf(out, "  post_cfg_hook: %s\n", app.PostCfgHook)
			}
		}
	case "show", "status", "install", "update", "update cfg", "remove-extras", "operations":
		path, err := manifest.SelectionPath()
		if err != nil {
			return err
		}
		config, err := data.Selection(path)
		if err != nil {
			return err
		}
		resolved, err := data.Resolve(config.Apps)
		if err != nil {
			return err
		}
		if command != "show" && command != "status" {
			return app.runOperations(command, data, resolved, config, path, in, out, stderr)
		}
		if command == "status" {
			report, err := installed.Inspect(context.Background(), resolved)
			if err != nil {
				return err
			}
			if app.Verbose {
				printStatus(out, config.Name, path, config.Apps, report)
			} else {
				printCompactStatus(out, config.Name, report)
			}
			break
		}
		fmt.Fprintf(out,
			"Configuration: %s\nSelection file: %s\nSelected: %s\n"+
				"Desired applications (dependencies first):\n",
			config.Name, path, list(config.Apps))
		selected := make(map[string]bool)
		for _, id := range config.Apps {
			selected[id] = true
		}
		if len(resolved.Applications) == 0 {
			fmt.Fprintln(out, "  (none)")
		}
		for _, item := range resolved.Applications {
			reason := "dependency"
			if selected[item.ID] {
				reason = "selected"
			}
			fmt.Fprintf(out, "  %s [%s]\n", item.ID, reason)
		}
		fmt.Fprintln(out, "Nix packages (deduplicated descriptors):")
		if len(resolved.Packages) == 0 {
			fmt.Fprintln(out, "  (none)")
		}
		for _, p := range resolved.Packages {
			fmt.Fprintf(out, "  %s\n", p.Key())
		}
		fmt.Fprintln(out, "Configuration targets (owners):")
		if len(resolved.Targets) == 0 {
			fmt.Fprintln(out, "  (none)")
		}
		for _, target := range resolved.Targets {
			fmt.Fprintf(out, "  %s: %s\n", target.Path, list(target.Applications))
		}
		if config.Backend != "" {
			fmt.Fprintf(out, "Configuration source: %s %s\n", config.Backend, config.Repository)
		}
	}
	return nil
}

func (app *cli) runOperations(
	command string,
	data *manifest.Data,
	desired manifest.Resolved,
	config manifest.Configuration,
	selectionPath string,
	in io.Reader,
	out, stderr io.Writer,
) error {
	steps := [...]struct {
		name      string
		requested bool
	}{
		{"update", app.UpdateInstalled || command == "update" || command == "update cfg"},
		{"install", app.InstallMissing || command == "install"},
		{"remove-extras", app.RemoveExtra || command == "remove-extras"},
	}
	ctx, release, lockErr := installed.BeginMutation(context.Background())
	if lockErr != nil {
		return lockErr
	}
	defer release()
	var failures []error
	options := installed.ConfigurationOptions{
		ConfirmReplacement: confirmReplacement(in, stderr),
		Data:               data, Configuration: config.Name, SelectionPath: selectionPath,
	}
	if config.Backend != "" {
		options.Source = &manifest.ConfigurationSource{Backend: config.Backend, Repository: config.Repository}
	}
	for _, step := range steps {
		if !step.requested {
			continue
		}
		if app.Verbose {
			fmt.Fprintf(stderr, "Operation: %s\n", step.name)
		} else {
			fmt.Fprintf(stderr, "==> %s\n", commandName(command, step.name))
		}
		var report installed.Report
		var err error
		switch step.name {
		case "update":
			if command == "update cfg" {
				report, err = installed.Configure(ctx, desired, stderr, options)
			} else {
				report, err = installed.Update(ctx, desired, stderr, options)
			}
		case "install":
			report, err = installed.Install(ctx, desired, stderr, options)
		case "remove-extras":
			report, err = installed.RemoveExtras(ctx, data, desired, app.Yes, in, stderr)
		}
		if report.Profile != "" {
			if app.Verbose {
				printPackageChanges(out, commandName(command, step.name), report, err)
				fmt.Fprintf(out, "Operation result: %s\n", step.name)
				printStatus(out, config.Name, selectionPath, config.Apps, report)
			} else {
				printPackageChanges(out, commandName(command, step.name), report, err)
			}
		}
		printConfigurationMode(out, report.Configuration, app.Verbose)
		for _, stream := range []io.Writer{out, stderr} {
			if writer, ok := stream.(*checkedWriter); ok && writer.err != nil {
				err = &installed.OperationError{Severity: installed.SeverityInternal, Err: errors.Join(err, writer.err)}
			}
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", step.name, err))
			if ExitCode(err) >= 2 {
				break
			}
		}
	}
	return errors.Join(failures...)
}

func printStatus(out io.Writer, name, path string, selected []string, report installed.Report) {
	fmt.Fprintf(out, "Configuration: %s\nSelection file: %s\nSelected: %s\n", name, path, list(selected))
	fmt.Fprintf(out, "Installed profile: %s\nDesired applications (dependencies first):\n", report.Profile)
	if len(report.Applications) == 0 {
		fmt.Fprintln(out, "  (none)")
	}
	for _, app := range report.Applications {
		fmt.Fprintf(out, "  %s [%s]\n", app.ID, app.State)
		readiness := "ready"
		if !app.Ready {
			readiness = "blocked by " + list(app.BlockedBy)
		}
		fmt.Fprintf(out, "    configuration readiness: %s\n", readiness)
		for _, pkg := range app.Packages {
			fmt.Fprintf(out, "    %s [%s]: %s\n", pkg.Descriptor, pkg.State, list(pkg.Entries))
		}
	}
	fmt.Fprintln(out, "Installed entries:")
	if len(report.Entries) == 0 {
		fmt.Fprintln(out, "  (none)")
	}
	for _, entry := range report.Entries {
		state := "active"
		if !entry.Active {
			state = "inactive"
		}
		update := "unavailable"
		if entry.Updateable {
			update = "available"
		}
		descriptor := entry.Descriptor
		if descriptor == "" {
			descriptor = "external"
		}
		outputs := "(not recorded)"
		if len(entry.Outputs) > 0 {
			outputs = list(entry.Outputs)
		} else if entry.OriginalURL != "" {
			outputs = "default"
		}
		fmt.Fprintf(out,
			"  %s [%s; %s]\n    attribute: %s\n    source: %s\n    URL: %s\n    outputs: %s\n"+
				"    store paths: %s\n    update recipe: %s\n",
			entry.Name, state, descriptor, entry.AttrPath, entry.OriginalURL, entry.URL,
			outputs, list(entry.StorePaths), update)
	}
	fmt.Fprintln(out, "Extras:")
	if len(report.Extras) == 0 {
		fmt.Fprintln(out, "  (none)")
	}
	for _, entry := range report.Extras {
		fmt.Fprintf(out, "  %s\n", entry.Name)
	}
}

func printCompactStatus(out io.Writer, name string, report installed.Report) {
	fmt.Fprintf(out, "Packages (%s):\n", name)
	if len(report.Applications) == 0 {
		fmt.Fprintln(out, "  No selected applications.")
	}
	for _, app := range report.Applications {
		shade := green
		if !app.Ready {
			shade = yellow
		}
		fmt.Fprintf(out, "  %s %s: %s", colored(out, "-", shade), app.ID, colored(out, app.State, shade))
		if !app.Ready {
			fmt.Fprintf(out, "; configuration blocked by %s", list(app.BlockedBy))
		}
		fmt.Fprintln(out)
	}
	if len(report.Extras) != 0 {
		fmt.Fprintf(out, "  %s %d extra package entries; review with status --verbose or remove-extras.\n",
			colored(out, "Warning:", yellow), len(report.Extras))
	}
}

func printPackageChanges(out io.Writer, command string, report installed.Report, operationErr error) {
	if command != "update cfg" {
		if len(report.PackageChanges) != 0 {
			fmt.Fprintln(out, "Packages:")
		}
		for _, change := range report.PackageChanges {
			name := change.Descriptor
			if name == "" {
				name = change.Name
			}
			fmt.Fprintf(out, "  %s %s: %s\n", colored(out, "-", green), name, change.Action)
		}
		if len(report.PackageChanges) == 0 {
			message := "Packages: no changes"
			if operationErr != nil {
				message = "Packages: no changes observed; operation failed"
			} else if command == "install" {
				message = "Packages: no new packages"
			} else if command == "remove-extras" {
				message = "Packages: no packages removed"
			}
			fmt.Fprintln(out, message)
		}
	}
	for _, app := range report.Applications {
		if !app.Ready {
			fmt.Fprintf(out, "  %s %s: %s; configuration blocked by %s\n",
				colored(out, "Warning:", yellow), app.ID, app.State, list(app.BlockedBy))
		}
	}
	if len(report.Extras) != 0 {
		fmt.Fprintf(out, "  %s %d extra package entries; review with status --verbose or remove-extras.\n",
			colored(out, "Warning:", yellow), len(report.Extras))
	}
}

func commandName(command, step string) string {
	if command == "update cfg" {
		return command
	}
	return step
}

func verboseOutput(out io.Writer) bool {
	writer, ok := out.(*checkedWriter)
	return ok && writer.verbose
}

func list(values []string) string {
	if len(values) == 0 {
		return "(none)"
	}
	return strings.Join(values, ", ")
}
