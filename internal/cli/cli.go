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
	Yes             bool   `help:"Approve all removals without prompting; requires -r or remove-extras"`
	// Kong requires a terminal command; this hidden default enables flag-only invocations.
	Operations   struct{}        `cmd:"" default:"1" hidden:""`
	Configs      configsCmd      `cmd:"" help:"List available configurations and their selected applications"`
	Apps         appsCmd         `cmd:"" help:"List catalog applications, logical dependencies and packages"`
	Show         showCmd         `cmd:"" help:"Show the local selection and its resolved applications/packages"`
	Status       statusCmd       `cmd:"" help:"Compare desired applications with the installed Nix profile"`
	Install      installCmd      `cmd:"" help:"Run hooks and install missing applications without updating or removing installed entries"`
	Update       updateCmd       `cmd:"" help:"Run hooks, update installed desired applications and complete dependencies; leave new applications and extras untouched"`
	RemoveExtras removeExtrasCmd `cmd:"" help:"Remove extra applications with individual confirmation; preserve desired packages"`
}

type configsCmd struct{}
type appsCmd struct{}
type showCmd struct{}
type statusCmd struct{}
type installCmd struct{}
type updateCmd struct{}
type removeExtrasCmd struct{}

// Run executes a CLI invocation using the supplied arguments and streams.
// It returns errors to the caller without exiting the process.
func Run(args []string, in io.Reader, out, stderr io.Writer) error {
	var app cli
	helpRequested := false
	parser, err := kong.New(&app, kong.Name("dotm"), kong.Description("Use a command or combine -i, -u, and -r. Operations always run in update, install, remove-extras order; stop on the first error without rollback."), kong.Writers(out, stderr), kong.Exit(func(code int) {
		if code == 0 {
			helpRequested = true
		}
	}))
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
	if app.Yes && !app.RemoveExtra && command != "remove-extras" {
		return fmt.Errorf("--yes requires -r or remove-extras")
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
			if app.PostHook != "" {
				fmt.Fprintf(out, "  post_hook: %s\n", app.PostHook)
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
			report, err := app.runOperations(command, data, resolved, in, stderr)
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
		fmt.Fprintf(out, "Configuration: %s\nSelection file: %s\nSelected: %s\nDesired applications (dependencies first):\n", config.Name, path, list(config.Apps))
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
	}
	return nil
}

func (app *cli) runOperations(command string, data *manifest.Data, desired manifest.Resolved, in io.Reader, stderr io.Writer) (installed.Report, error) {
	steps := [...]struct {
		name      string
		requested bool
	}{
		{"update", app.UpdateInstalled || command == "update"},
		{"install", app.InstallMissing || command == "install"},
		{"remove-extras", app.RemoveExtra || command == "remove-extras"},
	}
	ctx := context.Background()
	var report installed.Report
	for _, step := range steps {
		if !step.requested {
			continue
		}
		fmt.Fprintf(stderr, "Operation: %s\n", step.name)
		var err error
		switch step.name {
		case "update":
			report, err = installed.Update(ctx, desired, stderr)
		case "install":
			report, err = installed.Install(ctx, desired, stderr)
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
			descriptor = "unknown"
		}
		outputs := "(not recorded)"
		if len(entry.Outputs) > 0 {
			outputs = list(entry.Outputs)
		} else if entry.OriginalURL != "" {
			outputs = "default"
		}
		fmt.Fprintf(out, "  %s [%s; %s]\n    attribute: %s\n    source: %s\n    URL: %s\n    outputs: %s\n    store paths: %s\n    update recipe: %s\n", entry.Name, state, descriptor, entry.AttrPath, entry.OriginalURL, entry.URL, outputs, list(entry.StorePaths), update)
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
