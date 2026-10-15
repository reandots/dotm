# CLI contract

Assume `dotm` is already installed and available on PATH; invoke it directly as `dotm`. `dotm --help` and `dotm <command> --help` describe installed syntax. Long options use `--` and can precede/follow commands. Use the same `--data-dir /actual/shared/data` on every command when nondefault; this never relocates local selection/exclusions.

## Choose a command

| Command | Effect and prerequisites |
| --- | --- |
| `configs` | List named configurations/top-level selections; no local selection or Nix |
| `apps` | List catalog applications/dependencies/packages/targets/hooks; no local selection or Nix |
| `show` | Resolve local selection, dependency-first applications, deduplicated packages, owned targets; no Nix |
| `status` | Read-only native profile inspection, readiness, missing/partial packages, extras; not file-content status |
| `install` | Hooks, missing desired packages, then eligible configuration synchronization |
| `update` | Advance machine-local nixos-unstable source; upgrade installed desired applications and complete their components/dependencies; eligible configurations follow |
| `update cfg` | Eligible configuration synchronization only; no package work or package pre-hooks |
| `cfg lint [--source /absolute/checkout]` | Passive whole-source/settings compatibility; no manifests, local selection, Nix, templates, code execution, acquisition, or state writes |
| `files lint` | Declarations, native effective rules, policy freshness/binding/integration; evaluates trusted templates, does not publish persistent policy |
| `files refresh [--update-source]` | Check/publish local policy; explicit flag also acquires/updates source |
| `files status` | Refresh local policy and inventory files; no fetch or apply |
| `remove-extras [--yes]` | Explicitly approved extra native package entries only |
| `clean [--keep N] [--gc] [--dry-run] [--yes]` | History pruning and explicitly optional shared-store GC; no manifests/local selection |
| `init [--configuration NAME] [--repository URL]` | Prepare inputs, install only missing catalog-selected chezmoi/dependencies, set up source/policy; no destination application |
| `doctor` | Read-only inputs/source/policy, backend/host, and current-session diagnostics; no fixes |

There is no generic install/update dry run, positional `install <app>`, force-conflict option, or implicit removal. Init prepares an already installed dotm; it does not install dotm, Nix, or Git. Only missing chezmoi and dependencies may be installed with package pre-hooks, not the rest of the selected applications; no configuration hooks or destinations are applied. Init has no `--yes` bypass. Show/status inspect scope, not guaranteed backend success. Files commands require their workflow, do not inspect Nix/apply files/run application hooks, and may evaluate templates; only `cfg lint` is passive source lint.

## Install versus update

Healthy `install` adds only missing descriptors, completes partial multi-package applications, preserves installed entries/extras/source lock, and still runs repeatable hooks/configuration on a satisfied set. Ambiguous native entries fail instead of being silently repaired.

`update` advances the source lock and uses active installed desired package evidence. Partial applications, newly added components, and their full current transitive logical dependencies participate. A shared active descriptor may seed multiple applications. An installed companion does not seed its absent parent; inactive entries alone do not seed an update. Wholly absent independent applications remain for `install`. Extras keep versions. With no active managed candidates, package work is a no-op, but eligible desired configurations can still run. Report source revision advancement separately from actual package path/version changes.

Readiness requires all packages in an application's dependency closure; package-free applications need no installation marker. This does not mean configuration files are synchronized. `update cfg` skips applications missing required packages, rather than installing them.

Chezmoi follows the same selection, update and removal rules as any application. No special infrastructure protection exists. Install/update resolve and verify the current profile binary after package work, using a newly installed version without requiring profile PATH exposure. Package pre-hooks cannot assume a first-time package already exists. Backend incompatibility is a configuration-stage failure and retains completed package updates.

## Verify the requested change

- Manifests/selection: `configs`, `apps`, `show`; inspect actual IDs, closure, descriptors, targets, warnings.
- Packages: `status` before deployment, authorized `install`/`update`, then `status`; test the actual executable when availability matters.
- Source compatibility: `cfg lint` (or explicit absolute `--source`); a pass does not prove runtime template success.
- File declarations/exclusions: complete integration, `files refresh`, `files lint`, `files status`. Inventory refreshes persistent policy but does not apply files. On an already integrated setup, lint can first expose stale policy; refresh then recheck it.
- File deployment: authorized `update cfg` or full package operation; inspect stage outcomes/conflicts and `files status` afterward. An inventory finding or skipped missing-package application may still exit 0.

If a prerequisite is missing, report exactly which verification could not run; never substitute parser success for semantic CLI verification.

`doctor` checks manifests/selection/declarations, passive source/settings safety, owned integration/effective native rules and published binding/freshness, Git/chezmoi capability, Nix features/daemon/native profile, and current PATH/XDG paths. Independent findings continue; blocked checks name their missing dependencies and never count as passes. A genuinely absent first-run profile is informational; corrupt/dangling/inaccessible profiles fail. No install/fetch/recovery/hooks/apply or persistent dotm/source/policy/profile writes occur, and no `--fix` exists. Trusted templates can execute code; temporary policy/backend probes are cleaned up, while Nix may create ancillary runtime/cache state. Session findings describe this process, not fresh-login/GUI acceptance. Remedies belong to `init`, `files refresh`, explicit user corrections, or external host/session preparation.

## Remove extra packages

`remove-extras` never edits manifests. First inspect `show`/`status`. Desired/shared descriptors remain protected. Each extra catalog application is approved independently; a shared extra component is removed only if every extra using it is approved. Unknown/external entries and inactive entries of desired descriptors are reviewed separately. Entries are rechecked before one native removal batch.

Answers: `y`/`yes` approve, Enter or `n`/`no` decline, `q`/`quit` cancel. EOF/input errors cancel the whole collected plan; redirected answers need complete lines. Use `--yes` only for explicit approval of all removals. Removal neither changes source lock, prunes history, deletes store objects, nor undoes file/hook effects. A failed native operation may have partial effects; inspect actual state, not an assumed rollback.

## Clean disk/history

Start with `clean --dry-run`. Default keeps active generation plus the two highest-numbered **other** generations, possibly newer than active after rollback. `--keep N` counts additional generations, not total; `--keep 0` removes rollback alternatives. Only standard XDG profile history is pruned. Changed history invalidates approval.

Without `--gc`, history pruning may reclaim no space. `--gc` permits collection across the shared Nix store, including unrelated unrooted objects. Other GC roots and retained generations protect rooted objects. `--dry-run --gc` previews garbage under current roots, not additional garbage hypothetical pruning would expose. Use GC only on explicit request; no automatic restoration of pruned history exists.

## Combined flags

`-i`/`--install`, `-u`/`--update`, `-r`/`--remove-extras` may be separate or clustered (`-iur`). Execution is always update → install → remove-extras, once each, independent of order/repetition. Never combine these flags with a subcommand. `--yes` requires removal or standalone clean and never approves source replacement. Clean is not included. Use individual subcommands with `&&` for a different order.

Manifests/selection load once but each cycle inspects current packages and retains its own hooks/configuration cycle. Configuration failures allow later operations; package/pre-hook/internal failures stop them. Maximum exit severity remains; earlier changes are not undone.

## Concurrency and output

One mutation per user's XDG state: `init`, install/update/update cfg/files refresh/files status/remove-extras/non-dry-run clean fail immediately when busy. Combined operations hold ownership through prompts/hooks; a nested hook mutation fails rather than waiting. Read-only inspection and cleanup dry-run are not atomic snapshots.

Do not delete `/tmp/dotm-<uid>/` lock files. Native update/add/recovery children can retain ownership after dotm dies; hooks do not, so stop surviving hooks before retry. Direct Nix/Git/chezmoi operations and independent state directories sharing a source are not coordinated.
stdout contains warnings, inspection/status, and actual nonfailure stage/target outcomes. Error diagnostics are retained in the returned failure chain and printed once on stderr with the original cause; do not expect the same error to be repeated on stdout. A nonzero exit may follow successful package/file changes. Use Recovery to interpret it.
