# Commands and workflows

[Documentation](README.md) · [Русский](../ru/usage.md)

Examples use the default data directory. Add `--data-dir /path/to/data` for another shared catalog. Global options such as `--data-dir`, `--verbose`, and `--yes` may appear before or after a command. Use `dotm help` to browse commands, `dotm help files` for nested help, or `dotm <command> --help` for command syntax. Help is grouped compactly; examples appear with relevant commands.

## Shell completion

Generate a shell completion script on standard output with Cobra's built-in command. dotm does not install scripts or edit shell startup files. Use `dotm completion bash --help` (or your shell name) for setup examples:

```sh
dotm completion bash > dotm.bash
dotm completion zsh > dotm.zsh
dotm completion fish > dotm.fish
dotm completion powershell > dotm.ps1
```

For a temporary Bash or Zsh session, source the generated script directly:

```sh
source <(dotm completion bash)
source <(dotm completion zsh)
```

For persistent use, install the generated script according to the shell's completion-directory conventions and configure the shell to load it (for example, Bash's completion.d or Zsh's `$fpath` with `compinit`). Fish and PowerShell likewise use their standard completion-script locations/loading mechanisms. Consult your shell's documentation for the appropriate per-user location; dotm does not make persistent shell changes.

## Output and verbosity

Default mutating `install`, `update`, and `remove-extras` reports describe package effects observed during this invocation, not the full installed inventory. They explicitly say when no package changed; unchanged installed inventory is available only with global `-v` / `--verbose`. Configuration work gives one overall completion/failure summary rather than per-application success boilerplate; it names changed destination paths and source changes, while unchanged successful paths and successful ignore exclusions stay in verbose details. Completing a configuration cycle or running hooks does not mean files changed. Verbose mode adds detailed reports:

```sh
./dotm install --verbose
./dotm -v status
./dotm -viur
```

Success, warning, and failure labels use color only in a terminal; set `NO_COLOR` to disable it. Redirected output has no color escapes. Errors, warnings, dependency blockers, retained backups, partial effects, and actual native/hook output remain visible without `--verbose`. `status`, `show`, `configs`, `apps`, `doctor`, and lint/check commands retain the state or findings requested; verbosity adds detail, not a substitute for the requested report. Package readiness does not prove configuration synchronization.

## Command reference

| Command | Behavior |
| --- | --- |
| `configs` | List named configurations and top-level selections; no local selection or Nix required |
| `apps` | List catalog applications, dependencies, packages, targets, and hook paths; no local selection or Nix required |
| `show` | Resolve local selection, dependency-first application set, deduplicated packages, and owned targets; no Nix required |
| `status` | Inspect packages and readiness without mutation; not a file-content check |
| `install` | Run hooks, add missing desired packages, then synchronize eligible configurations |
| `update` | Advance the source and update installed desired applications plus their complete logical dependencies; synchronize all eligible desired configurations |
| `update cfg` | Synchronize eligible configurations without package work or package pre-hooks; reports one overall configuration outcome, not package inventory |
| `cfg lint [--source /absolute/checkout]` | Passive local source/settings validation; no manifests, selection, Nix, or chezmoi executable required |
| `files lint` | Check declarations, effective native rules, and published policy integration |
| `files refresh [--update-source]` | Build/check/publish policy; only the explicit flag acquires/updates source |
| `files status` | Refresh policy locally and inventory managed changes/unclassified files; never fetch |
| `remove-extras [--yes]` | Remove explicitly approved extras, retaining desired/shared descriptors |
| `init [--configuration NAME] [--repository URL]` | Prepare inputs, acquire missing catalog-selected chezmoi/dependencies, set up source/integration/policy; no destination application |
| `doctor` | Read-only dotm/source/policy, backend, host, and current-session diagnostics; no repairs |
| `clean [--keep N] [--gc] [--dry-run] [--yes]` | Separate history pruning and optional shared-store GC; no manifests/selection required |

The files commands evaluate trusted templates but do not apply configurations, run application hooks, or inspect Nix. See [chezmoi and files](chezmoi.md).

## Initialize dotm

`init` prepares an already running dotm, not the dotm executable. Prepare Nix/Git externally. Missing chezmoi is declared in the catalog, selected in the current configuration, and installed with only its dependency closure; custom declarations are preserved. Compatible external executables are reused without automatic selection/adoption. See [installation](installation.md#initialize-dotm).

Supply `--configuration` and `--repository` when local inputs do not already provide an unambiguous choice. The global `--data-dir` selects shared manifests, not machine-local settings. Init validates local inputs and paths before writes and collects separate approvals for local-intent changes and source replacement. It has no `--yes` bypass: cancellation/EOF fails safely.

Init prepares approved files, backend, checkout, compatible static settings, root ignore integration and policy, then checks normal discovery. Matching checkouts are validated without fetch/fast-forward; dirty content and native rules remain. Only missing chezmoi and dependencies may be installed, with their package pre-hooks; other selected applications, configuration hooks and destinations are not deployed. No auto-commits or silent origin changes occur. Unsafe integration requires manual correction. Review completed manifest/package/source effects and retained backups, then commit shared-manifest and checkout changes yourself. Later errors retain earlier effects; repeat after repair.

When init edits the checkout integration, it explicitly asks you to review and commit those changes, including `.chezmoiignore`, before source synchronization. This reminder also appears if a later setup stage fails. Ordinary `install`, `update`, `update cfg`, and `files refresh --update-source` require a clean source; init's permission to prepare a dirty matching checkout does not relax that rule.

## Diagnose setup

Run `./dotm doctor` after setup or when prerequisites fail; global `--data-dir` selects the shared manifests. It checks selection/declarations, passive source/settings safety, owned root integration, effective native rules and published policy binding/freshness, Git/chezmoi capabilities, Nix features/daemon/profile, and current PATH/XDG exposure. Independent checks continue after failures; dependency-blocked checks name the missing prerequisite and are not passes.

Doctor never installs, fetches, recovers packages, changes persistent settings/source/policy/profile, runs application hooks, or applies destinations. There is no `--fix`. Effective checks evaluate trusted templates, which are not sandboxed; temporary probes are removed. Nix may create ancillary runtime/cache state during native inspection. A genuinely absent first-run profile is informational, not equivalent to a corrupt/dangling profile. Session checks describe the current process only, not a fresh login or graphical session.

Failures appear once on stderr; warnings, results and blocked checks use stdout. Exit 0 means no failing findings, 1 means checked dotm/source/policy input failures, and 3 means host/execution/internal failures; the highest severity wins. Remedies distinguish `init` setup, `files refresh` regeneration, explicit input corrections, and external host/session preparation. Doctor does not install Nix or configure the session.

## Inspect and install

```sh
./dotm show
./dotm status
./dotm install
./dotm status
```

Status distinguishes installed, partial, missing, and package-free applications, dependency readiness, and removable extras. Use `status --verbose` for explicit native entries, descriptors, source URLs, and store paths. Configuration readiness includes the entire dependency closure; it is not an installation-history marker or proof that files are synchronized.

With healthy source state, installation adds only missing descriptors, including missing pieces of multi-package applications, and preserves installed entries, extras, and the source lock. Repeating a satisfied install still runs configured hooks/configuration phases; the default package report says no package changed rather than repeating the installed inventory. Configuration reporting summarizes the cycle and shows actual applied paths/source changes when reported, without implying that completed hooks or a cycle changed files. Ambiguous desired native entries fail rather than being automatically repaired.

## Update

```sh
./dotm update
# Retry only configuration work:
./dotm update cfg
```

Update advances the machine-local `nixos-unstable` lock. It uses active package evidence: partial applications are completed, and current components/transitive logical companions are included even if newly added. A shared active descriptor can seed several applications. An installed companion does not select its absent parent; wholly absent independent applications remain for `install`. Inactive entries alone do not seed updates. Extras retain their versions. With no installed desired applications, package work is a no-op, but eligible configuration work still runs.

Source-lock changes and package store-path changes are reported separately: advancing the source need not change package versions. Default update reports only package effects observed in this invocation and explicitly says when no package changed; the unchanged installed inventory requires `-v` / `--verbose`. Package/source failures attempt recovery; later configuration failures preserve completed package work. See [recovery](troubleshooting.md).

## Switch configurations and remove extras

Edit `configuration` in machine-local `config.toml`, review `show`/`status`, then run `install`. Selection alone does not remove packages or destination files. To remove extra packages:

```sh
./dotm remove-extras
# Deliberately approve all removals without prompts:
./dotm remove-extras --yes
```

Each extra catalog application is confirmed separately. Desired/shared components remain protected; a component shared by extras is removed only if every application using it is approved. Unknown/external entries and inactive entries of desired descriptors are reviewed individually. Accept with `y`/`yes`, reject with Enter or `n`/`no`, cancel with `q`/`quit`. EOF/input errors cancel the entire collected plan; redirected input needs complete lines for every prompt. Entries are rechecked before one native removal batch.

Removal does not change the lock, prune generations, delete store objects, or undo user-data effects. A native failure can leave partial changes; actual state is reported without automatic rollback/retry.

Chezmoi is an ordinary selected application: it updates within the selected scope and becomes removable when no longer desired/shared. Install/update resolve and probe the current profile executable after package work, using the new version even without profile PATH exposure. Backend incompatibility fails configuration work while retaining successful package changes.

## History and disk space

```sh
./dotm clean --dry-run
./dotm clean
./dotm clean --keep 1 --gc
```

Default retention is the active generation plus the two highest-numbered **other** generations. `--keep N` changes that additional count; `--keep 0` removes all rollback alternatives. After rollback, alternatives can be newer than the active generation. Only the standard XDG profile history is pruned.

Without `--gc`, removing history roots may free no package space. **`--gc` permits store-wide collection, including unrelated unrooted builds and caches.** Active/retained profiles and other GC roots protect rooted objects. `--dry-run --gc` previews garbage under current roots, not the additional garbage future pruning would expose.

Cleanup uses the same approval answers as removal; `--yes` bypasses approval, and a changed history invalidates the reviewed plan. Failures do not automatically restore pruned history. Native rollback to a retained generation is described in [troubleshooting](troubleshooting.md).

## Combined operations

```sh
./dotm -iur
./dotm -iur --yes
```

`-i`/`--install`, `-u`/`--update`, and `-r`/`--remove-extras` can be clustered or separate. Order is always **update → install → remove-extras**, each requested operation once, regardless of flag order/repetition. Do not mix these flags with a subcommand. `--yes` requires removal or standalone `clean`; it never approves source replacement. Cleanup is never included.

Manifests/selection are loaded once, but each operation inspects current package state. Install and update retain separate hook/configuration cycles. Configuration failures allow subsequent operations; package/pre-hook/internal failures stop the sequence. Maximum exit severity is retained; earlier effects are not undone. For another order, use individual subcommands joined with `&&`.

## Concurrency and output

One mutation per user's XDG state directory: `init`, `install`, `update`, `update cfg`, `files refresh`, `files status`, `remove-extras`, and non-dry-run `clean` fail immediately if ownership is busy. Combined operations hold it across prompts and hooks. Nested mutation from a hook fails instead of waiting. Read-only commands and cleanup dry runs remain available, but are not atomic snapshots.

Private Linux flock files live under `/tmp/dotm-<uid>/`; do not delete them while operations may be running. Native update/add/recovery children retain ownership if dotm dies; hooks do not, so stop surviving hooks before retrying. Direct Nix/Git/chezmoi mutations are not coordinated, nor are independent state directories sharing one source.

stdout carries inspection and actual operation results; stderr carries stage diagnostics and native/hook output. A nonzero result can follow successful package or file changes. See [exit codes and retries](troubleshooting.md).
