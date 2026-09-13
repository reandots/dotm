package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"dotm/internal/installed"
	"dotm/internal/manifest"

	"github.com/alecthomas/kong"
)

type cli struct {
	DataDir string     `help:"directory containing catalog.toml and configurations.toml" default:"."`
	Configs configsCmd `cmd:"" help:"List available configurations and their selected applications"`
	Apps    appsCmd    `cmd:"" help:"List catalog applications, logical dependencies and packages"`
	Show    showCmd    `cmd:"" help:"Show the local selection and its resolved applications/packages"`
	Status  statusCmd  `cmd:"" help:"Compare desired applications with the installed Nix profile"`
	Install installCmd `cmd:"" help:"Install missing applications without updating or removing installed entries"`
}

type configsCmd struct{}
type appsCmd struct{}
type showCmd struct{}
type statusCmd struct{}
type installCmd struct{}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "dotm:", err)
		os.Exit(1)
	}
}

func run(args []string, out, stderr io.Writer) error {
	var app cli
	helpRequested := false
	parser, err := kong.New(&app, kong.Name("dotm"), kong.Writers(out, stderr), kong.Exit(func(code int) {
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
	if ctx.Command() == "" {
		return fmt.Errorf("expected a command")
	}

	data, err := manifest.Load(app.DataDir)
	if err != nil {
		return err
	}
	switch ctx.Command() {
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
		}
	case "show", "status", "install":
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
		if ctx.Command() == "install" {
			report, err := installed.Install(context.Background(), resolved, stderr)
			if report.Profile != "" {
				printStatus(out, config.Name, path, config.Apps, report)
			}
			return err
		}
		if ctx.Command() == "status" {
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
