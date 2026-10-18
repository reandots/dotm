# Troubleshooting and recovery

[Documentation](README.md) · [Русский](../ru/troubleshooting.md)

## Exit codes and retry boundaries

| Code | Meaning |
| --- | --- |
| 0 | Success, including expected missing-package skips and ignored targets |
| 1 | Partial/complete configuration failure: hooks, source acquisition/policy, or conflicts |
| 2 | Application package or package pre-hook failure |
| 3 | dotm argument/settings/internal failure, including inspection, output, locking, or recovery |

`cfg lint` and `files` commands use 0, 1, or 3. Warnings and inventory findings alone return 0. Combined operations keep maximum severity; configuration failures permit later operations, package/internal failures stop them. No special code is promised for uncatchable termination.

Diagnostics are emitted once: stdout carries warnings, status, and actual stage/target outcomes; error diagnostics are returned to the CLI and printed once on stderr with their original cause. A warning-only lint can succeed; an error does not become a warning merely because it is also represented in a structured result. Lifecycle operations continue according to the existing severity rules.

`cfg lint` reports whole-source and settings findings without inventing declaration context. `files lint` may report genuine declaration problems with their source/application/field/path and related declaration. Integration, source/settings, and policy findings use their real path/context and message; they have no declaration unless one actually produced them. `files refresh` returns its errors to the caller and prints warnings/status only; files lifecycle errors likewise remain in the returned failure while stdout reports warnings and nonfailure target outcomes.

For a missing, malformed, or stale regular policy file, `files refresh` can regenerate it. If the reported policy path is not a regular file, remove/rename it only after reviewing it, then refresh. For missing initial chezmoi/source/owned integration setup, use `dotm init` after reviewing its independent approvals and effects. Unsafe, ambiguous, damaged, or user-modified integrations and incompatible settings require the specific correction reported; do not replace them by guesswork. Correct declaration errors in the named TOML input. There is no `doctor --fix` repair path.

Start broad read-only diagnosis with `dotm doctor` (use the actual `--data-dir` when nondefault). It preserves independent findings even when manifests or a backend fail and explicitly labels dependency-blocked checks. It uses exit 0/1/3 with maximum severity; a missing backend is not a successful policy check. Current-process PATH/XDG findings do not prove fresh-login or desktop integration. Effective policy checks execute trusted templates; there is no repair mode.

Use `status --verbose` for the detailed installed-entry report; ordinary status/lifecycle output is concise. `-v` / `--verbose` changes reporting only, not mutation scope or safety. Do not repeat an install/update merely to obtain more output without considering repeated hooks and retained earlier effects. Terminal colors are supplementary and disabled by `NO_COLOR` or redirection.

## Common diagnostics

| Symptom | Action |
| --- | --- |
| Missing manifest/selection | Provision both shared TOML files and local `config.toml`; use `--data-dir` only for shared inputs |
| Nix unavailable, wrong store/features, unwritable state | Fix host provisioning and user/session environment; dotm does not install Nix or elevate privileges |
| Packages installed but commands/desktop entries unavailable | Check profile bin/share exposure, then start a new session; installing a shell does not change login shell |
| Missing packages block configuration | Inspect full dependency closure; install missing applications, then retry configuration work |
| Dirty/diverged/detached source | Preserve work and resolve/publish it deliberately; dotm does not stash/reset/clean/rebase |
| Source replacement cannot prove publication | Inspect local objects/remote reachability; never delete unpublished work merely to pass a check |
| Missing/stale/disconnected file policy | Use `dotm init` for initial source/integration setup; correct any unsafe/ambiguous finding it reports, then `files refresh` for regenerable regular policy. Lint alone does not publish |
| Native `!` rule overrides generated exclusion | Revise the owning native rule; adding later positive ignores cannot override negation precedence |
| Local file/type/symlink conflict | Compare source and destination, preserve local changes, deliberately reconcile and retry; no force mode is provided |
| Another mutation owns the state | Wait for it to finish; stop surviving hooks/native/manual writers before retry, never delete lock files |
| Path outside destination or custom XDG mapping error | Provide compatible chezmoi source layout/destination; declaration expansion does not remap source entries |

Do not delete user data, generated sources, journals, or generations to force progress. Avoid external native profile/source mutations while dotm runs.

### Source has local changes after init

`init` can add the owned policy block to `.chezmoiignore` and explicitly asks you to review and commit the checkout diff. The next source-synchronizing command refuses a dirty checkout instead of discarding it. The refusal names the repository and quotes blocking changed, untracked, or ignored paths; empty untracked directories are also reported. Intended source changes belong in a user-reviewed commit. Move unrelated local data outside the checkout; Git cannot preserve empty directories in a commit. Do not run `git clean` or `reset --hard` as a shortcut. Local `files refresh` without `--update-source` does not fetch or impose the acquisition cleanliness gate.

## Package update recovery

Before source advancement/native upgrade/add, `update` saves a durable recovery snapshot outside the generated source. A later package/source or verification failure attempts to restore the initial native generation, generated source, and lock. Package work is committed **before** configuration stages; subsequent file/hook failures preserve that endpoint. Install is not an equivalent all-or-nothing transaction and retains package effects on failure.

Interrupted/failed recovery leaves a journal. The next `install`, `update`, `update cfg`, `remove-extras`, or non-dry-run `clean` restores pending update state before new mutation. Repair the reported prerequisite (for example daemon access), stop surviving writers, then repeat the requested command. Recovery failure blocks mutation and cleanup pruning. `files refresh/status` refuse pending recovery without invoking Nix; read-only commands/dry-run cleanup do not recover.

A subsequent normal `update` advances the source again; it does not resume the failed target. Manual native rollback neither restores the generated source/lock nor clears the recovery journal. Arbitrary hook/data changes are never restored. This is package/source consistency, not application-data health or a power-loss guarantee.

## Missing/corrupt generated state

Generated flakes/locks normally live in `${XDG_STATE_HOME:-$HOME/.local/state}/dotm/sources/<id>`, selected by `dotm/source-current`. An intact legacy `dotm/source` can remain usable until rotation.

With missing/corrupt source, lock, or current pointer, existing profile entries become **external**, not proof that desired packages are installed. `update` has no managed package candidates but can still run eligible package-free configuration work. `install` creates a unique source locked to current `nixos-unstable` and installs the whole desired package set while preserving old entries/source paths. File collisions fail explicitly: review and remove conflicting external entries through `remove-extras` before retrying. Valid manifests/selection/native profile and successful pending recovery remain required; corruption does not bypass those gates.

Chezmoi follows ordinary application rules, including generated-state loss and removal. Historical `dotm/infrastructure.json` files are ignored and not automatically deleted. Init refuses pending package recovery; complete recovery through an ordinary package operation first. Completed manifest/package effects survive later setup failures. If an installed/updated backend is incompatible, repair the catalog/package or executable and retry configuration work; configuration failures do not undo successful package changes.

## Retained-generation rollback

Run only with dotm/native mutations stopped:

```sh
P="${XDG_STATE_HOME:-$HOME/.local/state}/nix/profile"
nix profile history --profile "$P"
# Replace N with an actual retained generation number:
nix profile rollback --profile "$P" --to N
```

A generation is the whole profile. Rollback restores packages, not the source lock, catalog, selection, configuration files, hook effects, or user data; it runs no hooks. Pruned generations are not supported recovery targets. Keep rollback alternatives when cleaning history. See [cleanup](usage.md).

## Interrupted chezmoi handoff

The sibling marker is normally `.chezmoi.dotm-handoff`, containing staged `new` and/or preserved `old`. dotm refuses an existing marker rather than overwriting ambiguous recovery data. A completed switch retains an old checkout under a reported `.chezmoi.dotm-backup-…/handoff/old` path.

Stop surviving Git/dotm/manual mutations first. Inspect and preserve both copies. Restore `old` only if the source path is absent; if the new source is already active, retain the marker separately as a backup after inspection. Moving/removing a marker is a deliberate manual recovery decision, not an automatic cleanup recipe. Do not blindly delete it. The protocol does not promise power-loss fsync durability or locking against manual writers. Detailed boundaries: [source recovery contract](../../.agent/chezmoi-backend.md#interrupted-handoff-and-recovery).

## Exercised environments and known limits

- Package workflow: ordinary untrusted multi-user Nix 2.35.6 user on Arch x86_64 and disposable Ubuntu 22.04.4 amd64; original example executables, selected jq man output, install/update/removal/hooks, combined cycles, rollback, and isolated daemon GC were exercised. A real source advance can leave package store paths unchanged.
- Configuration/files: isolated compiled-process scenarios with real Git and chezmoi v2.74.1, plus composed lifecycle on Arch Nix 2.35.6. Earlier Ubuntu package acceptance does not imply Ubuntu configuration acceptance.
- Automatic update recovery and generated-state repair have isolated regression/controlled-Nix CLI coverage; earlier real-Nix updates do not prove the newer interruption/recovery or source-rotation paths. Physical power-loss recovery is unverified.
- Fresh login, GUI/desktop discovery, Fish, other architectures, uncached builds, and other backend versions remain unverified. GUI/session installation does not provision a working session.

No live user state or host-store garbage was changed by those acceptance scenarios. Exact historical evidence and reproduction limits: [package acceptance](../../.agent/acceptance.md), [configuration acceptance](../../.agent/configuration-acceptance.md), and [CLI contracts](../../.agent/cli.md).
