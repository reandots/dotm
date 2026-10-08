package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"dotm/internal/installed"
	"dotm/internal/manifest"

	"github.com/alecthomas/kong"
)

type cli struct {
	DataDir         string `help:"directory containing catalog.toml and configurations.toml" default:"."`
	InstallMissing  bool   `name:"install" short:"i" help:"Run hooks and install missing desired applications without updating installed entries"`
	UpdateInstalled bool   `name:"update" short:"u" help:"Run hooks, update installed desired applications and complete their dependencies"`
	RemoveExtra     bool   `name:"remove-extras" short:"r" help:"Remove extras with individual confirmation after update/install"`
	Yes             bool   `help:"Approve removals or cleanup without prompting; requires -r, remove-extras, or clean"`
	// Kong requires a terminal command; this hidden default enables flag-only invocations.
	Operations   struct{}        `cmd:"" default:"1" hidden:""`
	Configs      configsCmd      `cmd:"" help:"List available configurations and their selected applications"`
	Apps         appsCmd         `cmd:"" help:"List catalog applications, dependencies, packages, configuration targets and hook paths"`
	Show         showCmd         `cmd:"" help:"Show the local selection and resolved applications, packages and owned configuration targets"`
	Status       statusCmd       `cmd:"" help:"Inspect the Nix profile and configuration readiness from current package requirements"`
	Install      installCmd      `cmd:"" help:"Run hooks and install missing applications without updating or removing installed entries"`
	Update       updateCmd       `cmd:"" help:"Run hooks, update installed desired applications and complete dependencies; leave new applications and extras untouched"`
	RemoveExtras removeExtrasCmd `cmd:"" help:"Remove extra applications with individual confirmation; preserve desired packages"`
	Clean        cleanCmd        `cmd:"" help:"Prune profile history; --gc explicitly collects the shared store"`
	Cfg          cfgCmd          `cmd:"" help:"Inspect local configuration source policy"`
	Files        filesCmd        `cmd:"" help:"Lint or refresh effective chezmoi file policy"`
}

type (
	configsCmd struct{}
	appsCmd    struct{}
	showCmd    struct{}
	statusCmd  struct{}
	installCmd struct{}
	updateCmd  struct {
		Run struct{} `cmd:"" default:"1" hidden:""`
		Cfg struct{} `cmd:"" help:"Synchronize eligible configurations without package changes or package pre-hooks"`
	}
	cfgCmd struct {
		Lint struct {
			Source string `help:"Absolute local checkout to inspect; defaults to the shared chezmoi source"`
		} `cmd:"" help:"Validate local source and effective chezmoi settings without downloads, execution, or writes"`
	}
	cleanCmd struct {
		Keep   int  `help:"Number of highest-numbered other generations to retain in addition to the active one" default:"2"`
		GC     bool `help:"Also collect unrooted objects throughout the shared Nix store, including objects unrelated to dotm"`
		DryRun bool `name:"dry-run" help:"Show the cleanup plan without deleting generations or collecting objects"`
	}
)

type filesCmd struct {
	Lint    struct{} `cmd:"" help:"Check declarations, trusted templates, and integration without persistent changes"`
	Refresh struct {
		UpdateSource bool `name:"update-source" help:"Acquire or update the shared checkout first (may fetch; dirty work is never discarded)"`
	} `cmd:"" help:"Publish and verify files policy; evaluates templates, --update-source may fetch"`
	Status struct{} `cmd:"" help:"Refresh local files policy and inspect filesystem status"`
}

type removeExtrasCmd struct{}

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
	helpRequested := false
	parser, err := kong.New(
		&app,
		kong.Name("dotm"),
		kong.Description("Use a command or combine -i, -u, and -r. "+
			"Operations always run in update, install, remove-extras order; "+
			"continue after configuration failures; stop after installation or internal failures. "+
			"Failed package updates attempt to restore initial package/source state; "+
			"incomplete recovery blocks later mutations; "+
			"earlier operations and arbitrary hook effects are not rolled back."),
		kong.Writers(out, stderr),
		kong.Exit(func(code int) {
			if code == 0 {
				helpRequested = true
			}
		}),
	)
	if err != nil {
		return err
	}
	ctx, err := parser.Parse(args)
	if err != nil {
		if helpRequested {
			return nil
		}
		return err
	}
	if helpRequested {
		return nil
	}
	command := ctx.Command()
	if command == "update run" {
		command = "update"
	}
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
	if command == "files lint" || command == "files refresh" || command == "files status" {
		return runFiles(command, app.Files, app.DataDir, in, out, stderr)
	}
	if command == "cfg lint" {
		return lintSource(app.Cfg.Lint.Source, out)
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
			printStatus(out, config.Name, path, config.Apps, report)
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
		if config.Configs != nil {
			fmt.Fprintf(out, "Configuration source: %s %s\n", config.Configs.Backend, config.Configs.Repository)
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
		Source: config.Configs, ConfirmReplacement: confirmReplacement(in, stderr),
		Data: data, Configuration: config.Name, SelectionPath: selectionPath,
	}
	for _, step := range steps {
		if !step.requested {
			continue
		}
		fmt.Fprintf(stderr, "Operation: %s\n", step.name)
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
			fmt.Fprintf(out, "Operation result: %s\n", step.name)
			printStatus(out, config.Name, selectionPath, config.Apps, report)
		}
		printConfiguration(out, report.Configuration)
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

func list(values []string) string {
	if len(values) == 0 {
		return "(none)"
	}
	return strings.Join(values, ", ")
}
