# chezmoi, filtering, and file inventory

[Documentation](README.md) · [Русский](../ru/chezmoi.md)

## Ownership and prerequisites

The selected configuration sets `backend = "chezmoi"` and the Git `repository` directly alongside its `name` and `apps`. dotm uses one shared checkout at `${XDG_DATA_HOME:-$HOME/.local/share}/chezmoi`, normal chezmoi configuration/cache/persistent state, and its effective destination. There is no second private environment. Git must be available; `dotm init` reuses compatible chezmoi or declares/selects and installs the ordinary `chezmoi` application and dependencies. It applies no destination files or configuration hooks. chezmoi v2.74.1 is the exercised baseline, not a proven minimum.

Only normal `mode = "file"` is supported. Declared paths must map inside effective `destDir`. Expanding `$XDG_CONFIG_HOME` in a declaration does **not** relocate source `dot_config`: it still maps according to chezmoi's layout. Do not solve this by broadening the destination to `/`; provide a compatible source layout/settings.

## Adopt the external policy

This integration is required before dotm applies configuration targets. `dotm init` installs its owned root block during initial setup; your dotfiles repository remains yours to review and commit. Corrective changes to unsafe, ambiguous, or modified integrations remain deliberate user work.

1. Run `dotm init --configuration NAME --repository URL` to prepare the source and dotm-owned root integration. Pass global `--data-dir /path/to/data` before `init` when shared manifests are nondefault. Init preserves native rules and rejects ambiguous or modified integration rather than rewriting it; review and commit the resulting checkout diff yourself. If correcting a rejected integration, adopt the current root inclusion from [examples/chezmoi-policy/.chezmoiignore](../../examples/chezmoi-policy/.chezmoiignore) into `.chezmoiignore` in the effective source root (including `.chezmoiroot`), retaining existing rules.
2. Init creates minimal static TOML at the standard XDG user config path only when no normal chezmoi configuration is selected and no incompatible selection exists. Compatible existing settings and unrelated data are preserved; review/correct incompatible settings as reported. No `data.dotm.policyFile` setting is required: the policy is discovered at `${XDG_STATE_HOME:-$HOME/.local/state}/dotm/chezmoi-policy.json`.
3. Run `dotm files refresh` to publish and validate the policy, then `dotm files lint` to verify normal integration. For later source acquisition/update, use `dotm files refresh --update-source` only when intentional.

The fixed state path follows the effective `XDG_STATE_HOME`, or `$HOME/.local/state` when it is unset or empty. A nonempty `XDG_STATE_HOME` must be absolute; with it unset/empty, `HOME` must be absolute. The policy file is outside Git. Never hand-author or copy it between machines. It is bound to the repository origin, effective source/destination/configuration paths, and schema, so a mismatch never applies another binding's exclusions. Missing policy preserves standalone native chezmoi behavior; a present malformed policy is an error, not an empty policy. An empty exclusion set is valid.

If you previously configured `data.dotm.policyFile`, that key is now unused and may be removed at your convenience; dotm does not edit your chezmoi configuration. Existing old generated policy files are left untouched and are not discovered as a fallback. Do not delete user-owned or old files as part of adoption without your own review.

With a valid policy, standalone chezmoi uses the **last published selection**, not the current dotm selection automatically. Changes to selection/declarations/source/settings can make policy stale: refresh it. Ordinary add/edit on excluded paths may fail or ignore input; exclusions are not a universal sandbox or an allowlist preventing addition of unrelated files.

### Upgrade an existing installation

Run `dotm init` deliberately against the existing manifests and matching repository; it does not fetch that checkout. Init recognizes the exact supported legacy inclusion and upgrades it to the owned portable root block, preserving native rules, compatible settings, and unrelated source edits. A modified or ambiguous block needs explicit correction, not a forced refresh.

Review the reported source diff with `git -C /actual/chezmoi/source diff -- .chezmoiignore` (use the effective root when `.chezmoiroot` is present). Review unrelated changes separately and commit/publish the integration yourself before ordinary lifecycle synchronization, which requires a clean, published source. Init does not commit or discard those changes.

The old `data.dotm.policyFile` key is unnecessary; the generated policy now uses the fixed XDG state path above. Neither the unused key nor old generated files are silently deleted. Do not copy an old policy into the new location: regenerate it with init or `files refresh`.

After adoption, run `dotm doctor`, `dotm cfg lint`, `dotm files lint`, and `dotm files status`. Doctor is read-only; status publishes local policy but does not apply destinations. Ordinary refresh/install/update/update cfg never upgrade the root integration or rewrite static settings. A new selection needs policy refresh; standalone chezmoi otherwise retains the last published selection.


## Acquisition and source switches

Install/update/update cfg acquire or fast-forward the source before applying eligible targets. A missing source is staged and validated before activation. Existing sources require a clean standalone nonempty Git checkout, a branch with one origin upstream, and safe source layout. Dirty tracked/index/untracked/ignored content, including empty untracked directories, blocks synchronization. Detached, ahead/diverged, shallow, linked/shared/sparse/submodule checkouts and hidden-index state are rejected. No automatic stash, reset, clean, rebase, or source repair occurs.

A different repository requires a separate prompt. Only a complete lowercase **`yes` followed by newline** approves replacement; other answers, EOF, or unterminated input refuse it. Removal/cleanup `--yes` is unrelated. Before prompting, dotm proves old local objects are published in advertised origin history, conservatively including branches, reflogs, and dangling objects. Unavailable proof blocks replacement. URL identity does not guess SSH/HTTPS equivalence.

Old checkouts are retained in a reported backup path, not deleted automatically. Existing destination files are not removed when switching source/profile. Interrupted handoff requires deliberate recovery; see [troubleshooting](troubleshooting.md).

## Validation and application

```sh
./dotm cfg lint
./dotm cfg lint --source /absolute/local/checkout
./dotm update cfg
```

`cfg lint` passively checks the **whole** local effective source/settings, even outside selected applications and native ignores. It fetches nothing, evaluates no templates, runs no code, and writes no state. It needs neither manifests nor the chezmoi executable. It rejects run/modify/remove/exact actions, automatic externals, configured hooks and Git autoAdd/autoCommit/autoPush, conflicting sourceDir, `mode=symlink`, dynamic configuration templates, and source directory symlinks. Ordinary trusted file templates/partials and destination `symlink_` entries are distinct and can be supported. Static configuration/data syntax is checked. These are dotm's conservative compatibility limits, not general chezmoi restrictions.

Passing passive validation does not prove template correctness or apply success. Effective file checks/application evaluate trusted templates, whose helpers may execute commands. Git configuration, transport/filter helpers, and credentials are trusted too.

Configuration readiness requires complete application/dependency packages; package-free applications need no installation marker. `update cfg` skips missing-package applications rather than installing packages. Eligible configuration hooks repeat even on unchanged state. An empty target set never becomes an unscoped apply.

Application is per selected managed entry with native conflict protection, never forced overwrite/merge or broad cleanup. Incompatible types and symlink ancestors are conflicts. Local edits, first-adoption differences, and permission differences are preserved; safe independent files may already have changed. Native ignores remain effective. Conflicts block affected/consumer post-hooks, not all independent work. There is no destination/hook rollback.

## File-policy commands

```sh
./dotm files lint
./dotm files refresh
./dotm files refresh --update-source
./dotm files status
```

All use the desired dependency closure, independent of installed-package readiness, plus optional [local files.toml](configuration.md). Generated exclusions cover nonselected applications' configs, selected applications' unmanaged data, and local acknowledgements. They are literal roots/subtrees, including future descendants, not a broad allowlist.

`lint` checks declarations, native effective rules, policy freshness/binding, and actual integration without persistent publication. Native `!` rules intersecting generated exclusions fail even for absent paths; wholly native-hidden selected configurations warn. Warnings permit continuation, errors block application. It is not passive lint: templates are evaluated and temporary probes can be created.

`refresh` checks a new candidate without requiring old policy freshness, then atomically replaces its JSON and verifies normal integration. Failure before publication leaves old policy; failure after publication does not roll it back. `--update-source` explicitly adds acquisition/fetch and its safety/confirmation rules. `status` performs the same local refresh but never fetches. Both hold mutation ownership; pending package recovery blocks them without invoking Nix. Read-only lint does not recover.

Install/update/update cfg refresh this policy after pre-configuration hooks and source acquisition, before scoped apply. Policy errors preserve already committed package work.

## Inventory interpretation

`files status` separates **local changes**, **pending chezmoi changes**, and **untracked files**, retaining both status directions and owners. Empty sections are omitted. Terminal output uses styling unless `NO_COLOR` is set; redirected output remains plain, with escaped unusual paths.

Untracked means **unclassified**, not newly created or safe to delete. Classification acknowledges native ignores, selected application unmanaged data, and local exclusions. Other profiles' exclusions do not acknowledge their existing leftovers; unknown siblings under selected config directories remain candidates. Inventory covers HOME and supported XDG roots within effective destination, treats symlinks as leaves, and fails on incomplete collection instead of claiming a clean result. It never applies files or runs application hooks, but it intentionally updates persistent filtering policy and evaluates trusted templates.
