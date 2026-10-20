package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

const operationHelp = "Combined operations run in this order: update, install, remove-extras.\n" +
	"A config error allows the next operation to run.\n" +
	"A package or internal error stops the remaining operations.\n\n" +
	"If a package update fails, dotm tries to restore the packages and source.\n" +
	"If this fails, recover the failed update before making more changes.\n" +
	"Earlier operations and hook effects are not undone."

func (app *cli) command(selected *string) *cobra.Command {
	root := &cobra.Command{
		Use:   "dotm",
		Short: "Manage applications and config files",
		Long: "Manage applications with Nix and config files with chezmoi.\n\n" +
			"Use a command, or combine -i, -u, and -r.\n" +
			"Combined operations always update, install, then remove extra packages.",
		Example: "  dotm init\n  dotm status\n  dotm install\n" +
			"  dotm update cfg\n  dotm -ui\n  dotm completion zsh",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddGroup(
		&cobra.Group{ID: "inspect", Title: "View and check:"},
		&cobra.Group{ID: "manage", Title: "Set up and manage:"},
		&cobra.Group{ID: "config", Title: "Config files:"},
		&cobra.Group{ID: "other", Title: "Additional commands:"},
	)
	root.SetHelpCommandGroupID("other")
	root.SetCompletionCommandGroupID("other")
	bind := func(cmd *cobra.Command, name string) {
		cmd.Args = cobra.NoArgs
		cmd.RunE = func(_ *cobra.Command, _ []string) error {
			*selected = name
			return nil
		}
		// Commands without positional operands should not fall back to file completion.
		cmd.ValidArgsFunction = cobra.NoFileCompletions
	}
	bind(root, "operations")
	root.Flags().BoolVarP(&app.InstallMissing, "install", "i", false, "Install missing applications")
	root.Flags().BoolVarP(&app.UpdateInstalled, "update", "u", false, "Update installed applications")
	root.Flags().BoolVarP(&app.RemoveExtra, "remove-extras", "r", false,
		"Remove extra packages; ask before each removal")
	root.PersistentFlags().StringVar(&app.DataDir, "data-dir", "",
		"Directory with catalog.toml and configurations.toml\n(default: ${XDG_CONFIG_HOME:-$HOME/.config}/dotm/data)")
	root.PersistentFlags().BoolVarP(&app.Verbose, "verbose", "v", false, "Show more details")
	root.PersistentFlags().BoolVar(&app.Yes, "yes", false, "Skip removal prompts (only with -r, remove-extras, clean)")
	// The flag exists above; this only adds Cobra's directory-completion annotation.
	_ = root.MarkPersistentFlagDirname("data-dir")

	add := func(parent *cobra.Command, use, short, group, name string) *cobra.Command {
		cmd := &cobra.Command{Use: use, Short: short, GroupID: group}
		bind(cmd, name)
		parent.AddCommand(cmd)
		return cmd
	}
	add(root, "configs", "List configurations and their applications", "inspect", "configs")
	add(root, "apps", "List applications, dependencies, packages, and config paths", "inspect", "apps")
	add(root, "show", "Show the selected configuration and required applications", "inspect", "show")
	add(root, "status", "Show installed and missing packages", "inspect", "status")
	doctor := add(root, "doctor", "Check dotm setup, tools, and the current session", "inspect", "doctor")
	doctor.Long = "Check dotm files, required tools, host setup, and the current session.\n\n" +
		"Does not repair setup, install packages, or download sources.\n" +
		"Does not apply configs or recover failed updates."

	init := add(root, "init", "Set up dotm and chezmoi without applying config files", "manage", "init")
	init.Long = "Set up dotm files, chezmoi, and file rules.\n\n" +
		"Only installs chezmoi and its dependencies, if needed.\n" +
		"Does not apply config files.\n" +
		"Asks before changing existing settings or replacing a source checkout.\n" +
		"--yes does not skip these prompts."
	init.Example = "  dotm init\n  dotm init --configuration laptop --repository https://example.org/user/dotfiles.git"
	init.Flags().StringVar(&app.Init.Configuration, "configuration", "",
		"Configuration to select or create for new manifests")
	init.Flags().StringVar(&app.Init.Repository, "repository", "", "Git repository URL for the shared chezmoi source")

	install := add(root, "install", "Install missing applications and apply their configs", "manage", "install")
	install.Long = "Run hooks, install missing applications, and apply config files.\n" +
		"Only applies configs whose package requirements are met.\n\n" +
		"Does not update or remove installed packages.\n\n" + operationHelp
	update := add(root, "update", "Update installed applications and apply their configs", "manage", "update")
	update.Long = "Update installed applications and add their missing dependencies.\n" +
		"Run hooks and apply configs whose package requirements are met.\n\n" +
		"Does not install new independent applications or remove extra packages.\n" +
		"Use 'update cfg' to update config files only.\n\n" + operationHelp
	update.Example = "  dotm update\n  dotm update cfg"
	updateCfg := add(update, "cfg", "Update and apply config files without changing packages", "", "update cfg")
	updateCfg.Long = "Run config hooks, update the shared source, and refresh file rules.\n" +
		"Apply configs whose package requirements are met.\n\n" +
		"Does not change packages or run package hooks.\n" +
		"Recover any failed package update before using this command."
	remove := add(root, "remove-extras", "Remove extra packages; ask before each removal", "manage", "remove-extras")
	remove.Long = "Remove packages not needed by the selected configuration.\n" +
		"Ask before each removal unless --yes is set."
	remove.Example = "  dotm remove-extras\n  dotm remove-extras --yes"
	clean := add(root, "clean", "Remove old profile versions; use --gc to clean the shared store", "manage", "clean")
	clean.Long = "Remove old Nix profile versions.\n" +
		"Keep the active version and the number of other versions set by --keep.\n\n" +
		"Only --gc deletes unused objects from the shared Nix store.\n" +
		"This can affect objects not managed by dotm."
	clean.Example = "  dotm clean --dry-run\n  dotm clean --keep 2\n  dotm clean --gc"
	clean.Flags().IntVar(&app.Clean.Keep, "keep", 2, "Number of other profile versions to keep besides the active one")
	clean.Flags().BoolVar(&app.Clean.GC, "gc", false, "Also delete unused objects from the shared Nix store")
	clean.Flags().BoolVar(&app.Clean.DryRun, "dry-run", false, "Show the cleanup plan without deleting anything")

	cfg := add(root, "cfg", "Check local config source rules", "config", "")
	files := add(root, "files", "Check file rules and show changed or untracked files", "config", "")
	for _, group := range []*cobra.Command{cfg, files} {
		group.RunE = func(cmd *cobra.Command, _ []string) error {
			return fmt.Errorf("%s requires a subcommand; see '%s --help'", cmd.CommandPath(), cmd.CommandPath())
		}
	}
	lint := add(cfg, "lint", "Check local source and settings without running templates", "", "cfg lint")
	lint.Long = "Check the local source and chezmoi settings.\n" +
		"Does not download sources, run templates, or write files."
	lint.Flags().StringVar(&app.Cfg.Lint.Source, "source", "",
		"Absolute local source directory (default: shared chezmoi source)")
	_ = lint.MarkFlagDirname("source")
	add(files, "lint", "Check file rules, templates, and chezmoi setup", "", "files lint").Long =
		"Check file rules, templates, and chezmoi setup without saving changes.\n" +
			"Runs trusted templates: they can execute commands.\n" +
			"Use only sources you trust."
	refresh := add(files, "refresh", "Save and check file rules without applying config files", "", "files refresh")
	refresh.Long = "Save and check file rules. Runs trusted templates.\n" +
		"Does not apply config files.\n\n" +
		"Uses the local source by default. --update-source can download changes.\n" +
		"Commit and publish local source changes first; dotm never discards them."
	refresh.Flags().BoolVar(&app.Files.Refresh.UpdateSource, "update-source", false,
		"Download or update the shared source first")
	add(files, "status", "Refresh local file rules and show changed or untracked files", "", "files status").Long =
		"Save current file rules, then show changed, missing, and untracked files.\n\n" +
			"Does not download sources, apply configs, run hooks, or change packages.\n" +
			"Untracked files are not a list of files to delete."
	files.Example = "  dotm files lint\n  dotm files refresh --update-source\n  dotm files status"
	return root
}
