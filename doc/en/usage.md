# Commands and workflows

[Documentation](README.md) · [Русский](../ru/usage.md)

Examples use the default data directory. Add `--data-dir /path/to/data` for another shared catalog. Long options use `--` and may appear before or after a command. Use `./dotm --help` or `./dotm <command> --help` for syntax.

## Command reference

| Command | Behavior |
| --- | --- |
| `configs` | List named configurations and top-level selections; no local selection or Nix required |
| `apps` | List catalog applications, dependencies, packages, targets, and hook paths; no local selection or Nix required |
| `show` | Resolve local selection, dependency-first application set, deduplicated packages, and owned targets; no Nix required |
| `status` | Inspect packages and readiness without mutation; not a file-content check |
| `install` | Run hooks, add missing desired packages, then synchronize eligible configurations |
| `update` | Advance the source and update installed desired applications plus their complete logical dependencies; synchronize all eligible desired configurations |
| `update cfg` | Synchronize eligible configurations without package work or package pre-hooks |
| `cfg lint [--source /absolute/checkout]` | Passive local source/settings validation; no manifests, selection, Nix, or chezmoi executable required |
| `files lint` | Check declarations, effective native rules, and published policy integration |
| `files refresh [--update-source]` | Build/check/publish policy; only the explicit flag acquires/updates source |
| `files status` | Refresh policy locally and inventory managed changes/unclassified files; never fetch |
| `remove-extras [--yes]` | Remove explicitly approved extras, retaining desired descriptors |
| `clean [--keep N] [--gc] [--dry-run] [--yes]` | Separate history pruning and optional shared-store GC; no manifests/selection required |

The files commands evaluate trusted templates but do not apply configurations, run application hooks, or inspect Nix. See [chezmoi and files](chezmoi.md).

## Inspect and install

```sh
./dotm show
./dotm status
./dotm install
./dotm status
```

Status distinguishes installed, partial, missing, and package-free applications, explicit native entries, and extras. Configuration readiness includes the entire dependency closure; it is not an installation-history marker or proof that files are synchronized.

With healthy source state, installation adds only missing descriptors, including missing pieces of multi-package applications, and preserves installed entries, extras, and the source lock. Repeating a satisfied install still runs configured hooks/configuration phases. Ambiguous desired native entries fail rather than being automatically repaired.

## Update

```sh
./dotm update
# Retry only configuration work:
./dotm update cfg
```

Update advances the machine-local `nixos-unstable` lock. It uses active package evidence: partial applications are completed, and current components/transitive logical companions are included even if newly added. A shared active descriptor can seed several applications. An installed companion does not select its absent parent; wholly absent independent applications remain for `install`. Inactive entries alone do not seed updates. Extras retain their versions. With no installed desired applications, package work is a no-op, but eligible configuration work still runs.

Source-lock changes and package store-path changes are reported separately: advancing the source need not change package versions. Package/source failures attempt recovery; later configuration failures preserve completed package work. See [recovery](troubleshooting.md).

## Switch configurations and remove extras

Edit `configuration` in machine-local `config.toml`, review `show`/`status`, then run `install`. Selection alone does not remove packages or destination files. To remove extra packages:

```sh
./dotm remove-extras
# Deliberately approve all removals without prompts:
./dotm remove-extras --yes
```

Each extra catalog application is confirmed separately. Desired/shared components remain protected; a component shared by extras is removed only if every application using it is approved. Unknown/external entries and inactive entries of desired descriptors are reviewed individually. Accept with `y`/`yes`, reject with Enter or `n`/`no`, cancel with `q`/`quit`. EOF/input errors cancel the entire collected plan; redirected input needs complete lines for every prompt. Entries are rechecked before one native removal batch.

Removal does not change the lock, prune generations, delete store objects, or undo user-data effects. A native failure can leave partial changes; actual state is reported without automatic rollback/retry.

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

One mutation per user's XDG state directory: `install`, `update`, `update cfg`, `files refresh`, `files status`, `remove-extras`, and non-dry-run `clean` fail immediately if ownership is busy. Combined operations hold it across prompts and hooks. Nested mutation from a hook fails instead of waiting. Read-only commands and cleanup dry runs remain available, but are not atomic snapshots.

Private Linux flock files live under `/tmp/dotm-<uid>/`; do not delete them while operations may be running. Native update/add/recovery children retain ownership if dotm dies; hooks do not, so stop surviving hooks before retrying. Direct Nix/Git/chezmoi mutations are not coordinated, nor are independent state directories sharing one source.

stdout carries inspection and actual operation results; stderr carries stage diagnostics and native/hook output. A nonzero result can follow successful package or file changes. See [exit codes and retries](troubleshooting.md).
