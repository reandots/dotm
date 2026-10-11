# Configuration files, exclusions, and inventory

## Supported environment

Selections with config targets require per-selection `backend = "chezmoi"` and real `repository` settings. Git and chezmoi must already be available. dotm uses the shared source `${XDG_DATA_HOME:-$HOME/.local/share}/chezmoi`, normal chezmoi configuration/cache/persistent state, and its effective destination, not an isolated private environment. chezmoi v2.74.1 is an exercised baseline, not an established minimum.

Only normal `mode = "file"` is supported. Targets must map inside effective `destDir`. Catalog expansion of `$XDG_CONFIG_HOME` does not remap chezmoi `dot_config`; use a source layout/static settings compatible with actual custom XDG paths. Never broaden destination to `/` as a workaround.

`cfg lint` passively checks the **whole** effective source/settings, including unselected and native-ignored content. It needs no manifests, selection, Nix, or chezmoi executable and runs no templates/code, fetches nothing, and writes no state. `--source` must specify an absolute local checkout.

Rejected compatibility features: run/modify/remove/exact actions, automatic externals, configured hooks, Git autoAdd/autoCommit/autoPush, conflicting sourceDir, symlink mode, dynamic configuration templates, and source directory symlinks. Ordinary trusted file templates/partials and destination `symlink_` entries are distinct and can be supported. Static settings/data syntax is checked. Passing lint proves neither template behavior nor application success. Effective checks/application evaluate trusted templates whose helpers may execute commands; Git helpers/configuration/transports/credentials are also trusted.

## Initial external-policy integration

Required before configuration application. Make changes in the actual dotfiles/provisioning inputs, not an undocumented ephemeral setup.

1. Preserve existing native ignore rules. Add the following inclusion to `.chezmoiignore` in the **effective source root** (respect `.chezmoiroot`), retaining other root/nested rules:

   ```text
   {{- $d := default dict (get . "dotm") -}}
   {{- $f := default "" (get $d "policyFile") -}}
   {{- if and (not (default false (get $d "nativeOnly"))) $f (stat $f) -}}
   {{- $p := include $f | fromJson -}}
   {{- if and (eq (int $p.schemaVersion) 1) (eq $p.repository (trimSuffix "\n" (output "git" "-C" .chezmoi.sourceDir "remote" "get-url" "origin"))) (eq $p.sourceDir .chezmoi.sourceDir) (eq $p.destDir .chezmoi.destDir) (eq $p.configFile .chezmoi.configFile) -}}
   {{ range $p.patterns }}{{ . }}
   {{ end -}}
   {{- end -}}
   {{- end }}
   ```

2. Ensure normal static chezmoi settings select the shared source, correct destination, and `mode = "file"`; preserve unrelated data/settings.
3. Run `dotm files lint` and obtain its calculated **Policy path**. Initial missing policy/integration errors are expected, not successful validation.
4. In the normal chezmoi settings (typically `${XDG_CONFIG_HOME:-$HOME/.config}/chezmoi/chezmoi.toml`), bind that exact absolute path:

   ```toml
   [data.dotm]
   policyFile = "/absolute/path/reported/by/files-lint.json"
   ```

   Do not copy this placeholder or another machine's hash. Effective configuration/source-root/destination path changes require the newly calculated binding.
5. Commit/publish source changes and leave the source clean before source synchronization. `dotm files refresh` checks/publishes locally; `dotm files lint` verifies normal integration. If source acquisition is still needed, use `dotm files refresh --update-source` after preparing source/settings/integration; this introduces network/source-replacement checks. Then inspect `dotm files status`.

Generated policy is JSON under `${XDG_STATE_HOME:-$HOME/.local/state}/dotm/files/chezmoi/<key>.json`, outside Git. Never hand-edit it. It binds origin URL, effective source/destination/configuration paths, and schema; no separate repository ID exists.

Without published policy, standalone chezmoi retains native behavior. With it, standalone chezmoi follows the **last published** selection, not a new local selection automatically. Refresh after changes to selection/declarations/source/settings. Exclusions are literal roots/subtrees including future descendants, not a universal sandbox/allowlist against adding unrelated files. Ordinary chezmoi add/edit on excluded paths may ignore/reject input.

## Which exclusions mean what

- Nonselected applications' `configs`: exclude from synchronization, but do **not** acknowledge leftover files for inventory.
- Selected applications' `unmanaged`: acknowledge known application data and exclude synchronization/inventory.
- Local `files.toml`: acknowledge literal immediate children under expanded absolute parents; excludes synchronization/inventory.
- Native `.chezmoiignore`: preserve normal source behavior. Native `!` negations intersecting generated exclusions are errors even if the path is absent. Adding later positive ignores cannot override that precedence. Selected configs wholly hidden natively warn.

Never exclude an owned config as unmanaged to bypass a conflict. Warnings permit continuation; policy errors block configuration application.

## Policy commands and application

`files lint` checks declaration/native rules, freshness/binding, and actual integration without persistent publication. It is **not** passive: trusted templates run and temporary probes may be created.

`files refresh` validates a fresh candidate without requiring old-policy freshness, atomically publishes it, then verifies normal integration. Failure before publication preserves old policy; failure after publication does not roll it back. Only `--update-source` adds acquisition/fetch. `files status` refreshes locally without fetch. Neither command applies files/runs application hooks/inspects packages. Pending package recovery blocks refresh/status without invoking Nix; read-only lint does not recover.

`install`, `update`, and `update cfg` run source acquisition and policy refresh after eligible pre-configuration hooks, before scoped apply. Readiness requires all packages of the application/transitive dependencies; package-free apps need no marker. `update cfg` skips missing-package apps. Hooks repeat even when files do not change. An empty target set never becomes unscoped apply.

Files apply per selected managed entry with native conflict protection: no forced overwrite/merge or broad cleanup. Local first-adoption/content/permission differences, incompatible types, and symlink ancestors can conflict. Local data is preserved; independent safe files may already change. Conflicts block affected/consumer post-hooks, not all independent work. There is no file/hook rollback.

## Source acquisition and replacement

Install/update/update cfg acquire or fast-forward before eligible application; only explicit `files refresh --update-source` does so among file commands. Missing source is staged/validated before activation. Existing source must be a clean standalone nonempty Git checkout on a branch with one origin upstream. Dirty tracked/index/untracked/ignored content, including empty untracked directories, blocks synchronization. Detached, ahead/diverged, shallow, linked/shared/sparse/submodule checkouts and hidden-index state are rejected. No stash/reset/clean/rebase/repair is automatic.

Repository URL identity is exact; do not assume SSH/HTTPS equivalence. A URL switch requires proof old objects are published in advertised origin history, including conservatively branches/reflogs/dangling objects; unavailable proof blocks it. Approval is a complete lowercase `yes` plus newline. Other input/EOF/unterminated input refuses; `--yes` for removal/cleanup is unrelated. Obtain user authorization for replacement, do not pipe approval by default. Old source remains at a reported backup path; destination files are not deleted. Interrupted handoff needs deliberate recovery.

## Inventory interpretation

`files status` distinguishes local changes, pending chezmoi changes, and untracked files, with both status directions and owners. Untracked means **unclassified**, not new or disposable. Native ignores, selected unmanaged data, and local acknowledgements classify paths. Unknown siblings under selected directories can remain candidates. Other selections' exclusions do not acknowledge existing leftovers.

Inventory covers HOME and supported XDG roots inside effective destination, treats symlinks as leaves, and fails on incomplete collection rather than claiming cleanliness. Empty sections are omitted. `NO_COLOR` disables styling; redirected output is plain with escaped unusual paths. Findings alone can exit 0. Inventory publishes policy/evaluates templates but never applies files; do not describe it as wholly read-only.
