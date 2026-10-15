# Failures and recovery

## Read results before retrying

| Exit | Meaning |
| --- | --- |
| 0 | Success, also expected missing-package skips, ignored targets, warnings, and inventory findings |
| 1 | Partial/complete configuration failure: hooks, acquisition/policy, conflicts |
| 2 | Package or package pre-hook failure |
| 3 | Arguments/settings/internal/inspection/output/locking/recovery failure |

`cfg lint` and files commands use 0/1/3. Combined flags keep maximum severity; configuration failures permit later cycles, package/internal failures stop them. No specific exit is promised for uncatchable termination. Diagnostics are emitted once: stdout carries warnings and actual status/stage/target outcomes; errors stay in the returned failure chain and are printed once on stderr with their original cause. Nonzero does not imply no effects.

`cfg lint` reports source/settings context without manufacturing declaration provenance. Files declaration findings may include their actual source/application/field/path and related declaration; integration, source/settings, and policy findings use their actual path/context without a fake declaration. `files refresh` errors are returned while stdout carries warnings/status only; lifecycle file errors are returned too, while warnings and nonfailure target outcomes remain on stdout. Existing lifecycle continuation and exit codes are unchanged.

For a missing, malformed, or stale regular policy file, use `files refresh`; if the reported path is not a regular file, inspect it before deliberately renaming/removing it. For initial source and dotm-owned integration setup, use `dotm init` with the required configuration/repository inputs. If init rejects an unsafe, ambiguous, damaged, or user-modified integration, correct the reported issue in the effective source/settings; do not guess or replace unrelated rules. No `doctor --fix` exists.

## Diagnostic decisions

| Observation | User action |
| --- | --- |
| Missing manifest/selection | Use `dotm init` to create approved dotm input files and provide `--configuration` when needed; it does not download shared manifests. `--data-dir` moves shared inputs only |
| Nix unavailable/wrong store/features/unwritable state | Fix host/user provisioning externally; dotm does not install Nix or elevate |
| Installed package unavailable in session | Check standard XDG profile bin/share exposure and new session; installed shells do not change login shell |
| Missing packages skip configs | Inspect full dependency closure, use authorized `install`, retry configurations |
| Dirty/diverged/detached source | Preserve, reconcile, and publish work explicitly; no automatic stash/reset/clean/rebase |
| Source replacement lacks publication proof | Inspect local objects and remote reachability; do not delete unpublished work to pass |
| Missing/stale/disconnected policy | Use `dotm init` for initial source/integration setup; correct any unsafe/ambiguous finding it reports, then refresh regenerable regular policy and lint |
| Native `!` rule intersects exclusions | Revise the owning native rule, not later positive ignores |
| Local content/type/permission/symlink conflict | Compare source/destination, preserve changes, deliberately reconcile and retry |
| Busy mutation ownership | Let operation finish; stop surviving writers/hooks before retry; do not delete locks |
| Custom XDG path outside mapping/destination | Fix compatible source layout/settings, not destination `/` |

Do not delete generated sources, journals, user data, or generations to force progress. Avoid direct native profile/source mutations during dotm operations.

## Interrupted package update

Before source advancement/upgrade/add, update saves a recovery snapshot outside generated source. Package/source/verification failure attempts restoration of the initial native generation, generated source, and lock. Package work commits before configuration stages. Install is not the same all-or-nothing transaction and retains package effects on failure.

An interruption/failed recovery leaves a journal. The next install/update/update cfg/remove-extras/non-dry-run clean first restores pending update state. Fix reported prerequisites, stop surviving writers, and repeat the requested command. Recovery failure blocks mutation/pruning. Files refresh/status refuse pending recovery without invoking Nix; read-only inspection/dry-run cleanup do not recover.

A subsequent normal update advances source again; it does not resume a failed target. Manual rollback does not restore source lock or clear the journal. Hook/user-data changes are never recovered. This machinery is package/source consistency, not application-data health or a physical-power-loss guarantee.

## Missing/corrupt generated package state

Generated flakes/locks normally live at `${XDG_STATE_HOME:-$HOME/.local/state}/dotm/sources/<id>`, selected by `dotm/source-current`; intact legacy `dotm/source` can remain usable until rotation. These are generated state, not user catalogs.

Missing/corrupt source/lock/pointer makes existing entries external, not evidence of installed desired packages. Update has no managed package candidates, though eligible package-free configurations can run. Install creates a unique source at current nixos-unstable and installs the desired set, retaining old entries/source paths. Collisions fail explicitly: review conflicting external entries and, with authorization, remove through `remove-extras` before retry. Valid manifests/selection/native profile and successful pending recovery remain required.

Recorded infrastructure is the exception: its canonical descriptor/source evidence remains retained even when the current generated source is lost or rotated. A pending infrastructure-role record marks acquisition intent written before adding the backend; `init` can reconcile a completed add from canonical profile evidence without advancing packages. Preserve that state and native generations; do not guess ownership from names or delete the record to make the backend removable. Init refuses pending package recovery; use the existing package-operation recovery path first. Successful acquisition is not rolled back if later setup work fails.

## Retained generation rollback

Only with dotm/native writers stopped, inspect history:

```sh
P="${XDG_STATE_HOME:-$HOME/.local/state}/nix/profile"
nix profile history --profile "$P"
# Replace N with an actual retained generation, after reviewing scope:
nix profile rollback --profile "$P" --to N
```

This restores the **whole profile's packages**, not source lock/catalog/selection/files/hooks/data; it runs no hooks. It cannot restore pruned history. Do not use this instead of completing pending dotm update recovery.

## Interrupted chezmoi handoff

The sibling marker is normally `.chezmoi.dotm-handoff`, containing staged `new` and/or preserved `old`. dotm refuses existing ambiguous markers. A completed switch retains old source under a reported `.chezmoi.dotm-backup-…/handoff/old` path.

Stop surviving writers, inspect and preserve both copies. Restore `old` only if the source path is absent. If new source is active, retain the marker separately as a backup after inspection. Moving/removing a marker requires a deliberate recovery decision; never blindly delete it. Handoff does not promise power-loss durability or coordination with manual writers.

## Evidence limits

Package workflows were exercised as ordinary users with multi-user Nix 2.35.6 on Arch x86_64 and disposable Ubuntu 22.04 amd64. Configuration/files workflows used isolated real Git/chezmoi v2.74.1 and composed Arch Nix, not equivalent Ubuntu coverage. Controlled CLI tests cover automatic update recovery/source rotation; earlier real-Nix workflows do not prove power-loss recovery. Fresh login/GUI discovery, Fish, other architectures, uncached builds, and other backend versions remain unverified. Do not present baseline versions as minimum requirements or promise host/session provisioning.
