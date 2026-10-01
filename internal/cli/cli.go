package cli

import (
	"context"
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
}

type (
	configsCmd struct{}
	appsCmd    struct{}
	showCmd    struct{}
	statusCmd  struct{}
	installCmd struct{}
	updateCmd  struct{}
	cleanCmd   struct {
		Keep   int  `help:"Number of highest-numbered other generations to retain in addition to the active one" default:"2"`
		GC     bool `help:"Also collect unrooted objects throughout the shared Nix store, including objects unrelated to dotm"`
		DryRun bool `name:"dry-run" help:"Show the cleanup plan without deleting generations or collecting objects"`
	}
)
type removeExtrasCmd struct{}

// Run executes a CLI invocation using the supplied arguments and streams.
// It returns errors to the caller without exiting the process.
func Run(args []string, in io.Reader, out, stderr io.Writer) error {
	var app cli
	helpRequested := false
	parser, err := kong.New(
		&app,
		kong.Name("dotm"),
		kong.Description("Use a command or combine -i, -u, and -r. "+
			"Operations always run in update, install, remove-extras order; "+
			"stop on the first error. Failed updates attempt to restore initial package/source state; "+
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
	case "show", "status", "install", "update", "remove-extras", "operations":
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
			report, err := app.runOperations(command, data, resolved, config.Configs, in, stderr)
			if report.Profile != "" {
				printStatus(out, config.Name, path, config.Apps, report)
			}
			return err
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
	source *manifest.ConfigurationSource,
	in io.Reader,
	stderr io.Writer,
) (installed.Report, error) {
	steps := [...]struct {
		name      string
		requested bool
	}{
		{"update", app.UpdateInstalled || command == "update"},
		{"install", app.InstallMissing || command == "install"},
		{"remove-extras", app.RemoveExtra || command == "remove-extras"},
	}
	ctx, release, lockErr := installed.BeginMutation(context.Background())
	if lockErr != nil {
		return installed.Report{}, lockErr
	}
	defer release()
	var report installed.Report
	for _, step := range steps {
		if !step.requested {
			continue
		}
		fmt.Fprintf(stderr, "Operation: %s\n", step.name)
		var err error
		switch step.name {
		case "update":
			report, err = installed.Update(ctx, desired, stderr, installed.ConfigurationOptions{Source: source})
		case "install":
			report, err = installed.Install(ctx, desired, stderr, installed.ConfigurationOptions{Source: source})
		case "remove-extras":
			report, err = installed.RemoveExtras(ctx, data, desired, app.Yes, in, stderr)
		}
		if err != nil {
			return report, fmt.Errorf("%s: %w", step.name, err)
		}
	}
	return report, nil
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
