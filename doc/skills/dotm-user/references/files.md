# Configuration files, exclusions, and inventory

## Supported environment

Selections with config targets require per-selection `backend = "chezmoi"` and real `repository` settings. Git and chezmoi must already be available. dotm uses the shared source `${XDG_DATA_HOME:-$HOME/.local/share}/chezmoi`, normal chezmoi configuration/cache/persistent state, and its effective destination, not an isolated private environment. chezmoi v2.74.1 is an exercised baseline, not an established minimum.

Only normal `mode = "file"` is supported. Targets must map inside effective `destDir`. Catalog expansion of `$XDG_CONFIG_HOME` does not remap chezmoi `dot_config`; use a source layout/static settings compatible with actual custom XDG paths. Never broaden destination to `/` as a workaround.

`cfg lint` passively checks the **whole** effective source/settings, including unselected and native-ignored content. It needs no manifests, selection, Nix, or chezmoi executable and runs no templates/code, fetches nothing, and writes no state. `--source` must specify an absolute local checkout.

Rejected compatibility features: run/modify/remove/exact actions, automatic externals, configured hooks, Git autoAdd/autoCommit/autoPush, conflicting sourceDir, symlink mode, dynamic configuration templates, and source directory symlinks. Ordinary trusted file templates/partials and destination `symlink_` entries are distinct and can be supported. Static settings/data syntax is checked. Passing lint proves neither template behavior nor application success. Effective checks/application evaluate trusted templates whose helpers may execute commands; Git helpers/configuration/transports/credentials are also trusted.

## External-policy integration

Required before configuration application. Make changes in the actual dotfiles/provisioning inputs, not an undocumented ephemeral setup.

1. Manually adopt the following inclusion in `.chezmoiignore` in the **effective source root** (respect `.chezmoiroot`), preserving existing root/nested native rules. Replace the template in your own checkout when dotm's documented integration changes; dotm does not edit the checkout automatically.

   ```text
   {{- $d := default dict (get . "dotm") -}}
   {{- if not (default false (get $d "nativeOnly")) -}}
   {{- $state := env "XDG_STATE_HOME" -}}
   {{- if eq $state "" -}}
   {{- $home := env "HOME" -}}
   {{- if or (eq $home "") (not (isAbs $home)) -}}{{ fail "HOME must be an absolute path when XDG_STATE_HOME is unset" }}{{- end -}}
   {{- $state = printf "%s/.local/state" $home -}}
   {{- else if not (isAbs $state) -}}{{ fail "XDG_STATE_HOME must be an absolute path" }}{{- end -}}
   {{- $f := default "" (get $d "policyCandidate") -}}
   {{- if eq $f "" -}}{{- $f = printf "%s/dotm/chezmoi-policy.json" (trimSuffix "/" $state) -}}{{- end -}}
   {{- if stat $f -}}
   {{- $p := include $f | fromJson -}}
   {{- if and (eq (int $p.schemaVersion) 1) (eq $p.repository (trimSuffix "\n" (output "git" "-C" .chezmoi.sourceDir "remote" "get-url" "origin"))) (eq $p.sourceDir .chezmoi.sourceDir) (eq $p.destDir .chezmoi.destDir) (eq $p.configFile .chezmoi.configFile) -}}
   {{ range $p.patterns }}{{ . }}
   {{ end -}}
   {{- end -}}
   {{- end }}
   {{- end }}
   ```

   The `nativeOnly` and `policyCandidate` lookups are internal overrides used by dotm inspection/verification; do not configure them as user settings. Normal discovery reads only the fixed path. Missing policy preserves native behavior. `stat` guards absence only: malformed JSON is an error, not an empty policy. An empty `patterns` list is valid. The template checks schema, origin, source, destination, and config path before emitting literal patterns; a mismatch emits no exclusions from that policy.
2. Ensure normal static chezmoi settings select the shared source, correct destination, and `mode = "file"`; preserve unrelated data/settings. No `data.dotm.policyFile` key is required. Policy discovery uses `${XDG_STATE_HOME:-$HOME/.local/state}/dotm/chezmoi-policy.json`.
3. Run `dotm files refresh` to publish and validate, then `dotm files lint` to verify normal integration. Use `dotm files refresh --update-source` only when source acquisition is needed and approved.

The fixed state path is outside Git. If `XDG_STATE_HOME` is unset or empty, `HOME` must be absolute and the fallback is `$HOME/.local/state`; if set, `XDG_STATE_HOME` must be absolute. Never hand-author or copy policy between machines. A binding mismatch cannot apply another binding's exclusions; dotm reports integration/freshness problems during its checks.

If `data.dotm.policyFile` was configured previously, it is unused now and can be removed manually; dotm does not edit user configuration. Old generated policy files are retained, not discovered as a fallback. Do not delete old/user-owned files without review.

With valid policy, standalone chezmoi follows the **last published** selection, not a new local selection automatically. Refresh after changes to selection/declarations/source/settings. Exclusions are literal roots/subtrees including future descendants, not a universal sandbox/allowlist against adding unrelated files. Ordinary chezmoi add/edit on excluded paths may ignore/reject input.

## Which exclusions mean what

- Nonselected applications' `configs`: exclude from synchronization, but do **not** acknowledge leftover files for inventory.
- Selected applications' `unmanaged`: acknowledge known application data and exclude synchronization/inventory.
- Local `files.toml`: acknowledge literal immediate children under expanded absolute parents; excludes synchronization/inventory.
- Native `.chezmoiignore`: preserve normal source behavior. Native `!` negations intersecting generated exclusions are errors even if the path is absent. Adding later positive ignores cannot override that precedence. Selected configs wholly hidden natively warn.

Never exclude an owned config as unmanaged to bypass a conflict. Warnings permit continuation; policy errors block configuration application.

## Policy commands and application

`files lint` checks declaration/native rules, freshness/binding, and actual integration without persistent publication. It is **not** passive: trusted templates run and temporary probes may be created. Warnings go to stdout; error diagnostics are returned and emitted once on stderr with their original cause.

`files refresh` validates a fresh candidate without requiring old-policy freshness, atomically publishes it, then verifies normal integration. Failure before publication preserves old policy; failure after publication does not roll it back. Only `--update-source` adds acquisition/fetch. `files status` refreshes locally without fetch. Neither command applies files/runs application hooks/inspects packages. Pending package recovery blocks refresh/status without invoking Nix; read-only lint does not recover.

Refresh repairs only a missing, malformed, or stale **regular** policy file. If the reported policy path is not a regular file, inspect and deliberately move/remove that object before retrying. It does not repair a missing root inclusion, incompatible source layout, or incorrect chezmoi settings: manually adopt the current example inclusion at the effective source root (respecting `.chezmoiroot`) and correct source/settings. Declaration errors require editing the named manifest or local `files.toml`. There is no `doctor --fix` repair path.

`install`, `update`, and `update cfg` run source acquisition and policy refresh after eligible pre-configuration hooks, before scoped apply. Errors remain in the returned lifecycle failure; stdout contains warnings and nonfailure target outcomes. Policy errors preserve already committed package work.

Readiness requires all packages of the application/transitive dependencies; package-free apps need no marker. `update cfg` skips missing-package apps. Hooks repeat even when files do not change. An empty target set never becomes unscoped apply.

Files apply per selected managed entry with native conflict protection: no forced overwrite/merge or broad cleanup. Local first-adoption/content/permission differences, incompatible types, and symlink ancestors can conflict. Local data is preserved; independent safe files may already change. Conflicts block affected/consumer post-hooks, not all independent work. There is no file/hook rollback.

## Source acquisition and replacement

Install/update/update cfg acquire or fast-forward before eligible application; only explicit `files refresh --update-source` does so among file commands. Missing source is staged/validated before activation. Existing source must be a clean standalone nonempty Git checkout on a branch with one origin upstream. Dirty tracked/index/untracked/ignored content, including empty untracked directories, blocks synchronization. Detached, ahead/diverged, shallow, linked/shared/sparse/submodule checkouts and hidden-index state are rejected. No stash/reset/clean/rebase/repair is automatic.

Repository URL identity is exact; do not assume SSH/HTTPS equivalence. A URL switch requires proof old objects are published in advertised origin history, including conservatively branches/reflogs/dangling objects; unavailable proof blocks it. Approval is a complete lowercase `yes` plus newline. Other input/EOF/unterminated input refuses; `--yes` for removal/cleanup is unrelated. Obtain user authorization for replacement, do not pipe approval by default. Old source remains at a reported backup path; destination files are not deleted. Interrupted handoff needs deliberate recovery.

## Inventory interpretation

`files status` distinguishes local changes, pending chezmoi changes, and untracked files, with both status directions and owners. Untracked means **unclassified**, not new or disposable. Native ignores, selected unmanaged data, and local acknowledgements classify paths. Unknown siblings under selected directories can remain candidates. Other selections' exclusions do not acknowledge existing leftovers.

Inventory covers HOME and supported XDG roots inside effective destination, treats symlinks as leaves, and fails on incomplete collection rather than claiming cleanliness. Empty sections are omitted. `NO_COLOR` disables styling; redirected output is plain with escaped unusual paths. Findings alone can exit 0. Inventory publishes policy/evaluates templates but never applies files; do not describe it as wholly read-only.
