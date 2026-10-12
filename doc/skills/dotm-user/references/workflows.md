# User workflows

Commands below assume `dotm` is already installed and available on PATH. Invoke it directly and consistently pass `--data-dir /actual/shared/data` if nondefault. Do not write changes to this skill's examples instead of the user's inputs.

## Plan a complete application setup

Use this workflow when adding or setting up an application. Prefer a usable, reproducible setup covering the package, agreed configuration content, and classification of known application data; not every application needs custom settings, hooks, or file declarations.

1. Read the existing catalog/selection and inspect the application's relevant existing files and chezmoi source, respecting actual HOME/XDG paths. Preserve user content and reuse existing ownership. Consult application documentation or supported help when needed to establish configuration locations, syntax, activation requirements, and data/cache/state paths. Do not read secrets into the conversation or copy credentials into the source.
2. Present one concrete proposal before changing configuration content or file classification: package/catalog selection, destination paths for `configs`, useful settings to preserve or add with their purpose, and application-owned data/cache/state paths for `unmanaged`. Explain that `configs` manages files through chezmoi, whereas `unmanaged` excludes known data from synchronization and inventory; acknowledgement does not make data disposable. Identify paths actually observed versus documented candidates, and any source/backend prerequisites. Do not invent a path or setting to fill a field, exclude broad unrelated trees, or propose `files.toml` for application-owned data.
3. Ask the user to accept or adjust the concrete proposal, not to design TOML. If reliable candidates or preferences are missing, ask which existing files/settings they use or what behavior they want. Request a real source repository when needed; do not manufacture one. Existing explicit approval of the same setup is sufficient. If the user explicitly requests only the package, skip configuration discovery/proposals. Otherwise, unknown or declined optional configuration does not block authorized package work; leave it undeclared and report what remains undecided.
4. Implement the agreed setup end to end. Follow “Add application configuration files” for owned targets, actual source content, backend settings, and policy integration; follow “Choose an exclusion” for approved data paths. Prefer adopting existing configuration over replacing it with generic defaults. Add hooks only for a concrete required repeatable action, not as boilerplate; keep host/session/service provisioning outside this skill. A `configs` declaration alone neither creates settings nor activates them.
5. Follow the declaration/package checks below and the Files gates for changed files. Deploy only when authorized. After deployment, use a safe application-specific command or read-only check that exercises the agreed settings where available; report what it proves and any activation or verification limit. Do not call the result fully configured while agreed source content, file application, or required external activation remains incomplete. For edit-only requests, report prepared inputs and the next deployment/check commands instead of claiming runtime success.

## Add ripgrep (package-only example)

1. Read shared `catalog.toml`, `configurations.toml`, and local `config.toml`. Inspect the selected configuration and dependency closure. If ripgrep is already provided by an application such as `search`, reuse it instead of creating a redundant definition. An application ID need not equal its package or executable name.
2. Otherwise add a top-level catalog table (not a nested table accidentally belonging to the preceding application):

   ```toml
   [ripgrep]
   nix = ["ripgrep"]
   ```

3. Add `"ripgrep"` to the chosen configuration's existing `apps` array in `configurations.toml`, preserving other selections. Alternatively, add it to a selected aggregator's `dependencies` only when it should accompany that aggregator in **every** configuration using it. If it is already in the closure, no selection edit is needed. Merely declaring `[ripgrep]` installs nothing.
4. This example covers the package-only branch of the application setup workflow above. For a broader application request, first propose and agree any configuration content, `configs`, and `unmanaged`; add source settings or hooks only where the agreed setup requires them. `ripgrep` is the Nix attribute; `rg` is the upstream command. For other applications, establish the real nixpkgs attribute rather than assuming the executable name matches it.
5. Run `dotm configs`, `dotm apps`, and `dotm show`; verify the intended selection contains ripgrep and no unintended companions. With available Nix, run `dotm status` and review missing/partial/extras. These commands do not install it.
6. For an edit-only request, propose `dotm install`; do not substitute `update`, which does not seed wholly absent independent applications. If installation is authorized, run `dotm install`, then `dotm status`. Check `rg --version` in the user's configured session if command availability is part of the request. If the session PATH is not ready, checking the managed profile's `bin/rg --version` distinguishes package availability from session setup. Report which check ran.

`install` operates on the entire selected closure, not a positional application argument, and can run existing hooks/apply eligible configuration files. Do not promise that only the newly added package will be affected.

## New selection or switching

- Add a new `[[configurations]]` with unique `name` and `apps`; reuse catalog IDs. A configuration is a machine/use case, not a distribution.
- If any selected/transitive application owns config targets, add that named selection's real chezmoi backend/repository and complete Files integration before applying.
- Edit only `configuration = "name"` in local `config.toml`. Do not put the selection in `--data-dir` or invent automatic host detection.
- Review `show` and `status`, then propose/run `install` as authorized. Existing extra packages and old destination files remain. Remove extra packages only as a separate explicit decision through `remove-extras`; there is no automatic destination cleanup.

## Add application configuration files

- Identify actual destination file/directory paths and the owning application. Add `configs` without overlapping other catalog ownership or unmanaged declarations. Package-free config applications are valid.
- Put the corresponding files in the actual user's chezmoi Git source using its source naming/layout; a catalog destination declaration does not create source content. Source `dot_config` does not automatically follow custom `$XDG_CONFIG_HOME`.
- Ensure every selection whose closure now includes those targets has real backend/repository settings. Follow the Files reference for supported source content, static settings, policy inclusion/binding, publication, and conflicts.
- Use `cfg lint` for passive compatibility, then the Files policy/inventory checks. Propose `update cfg` if packages are already ready; otherwise `install`. Neither command force-overwrites local conflicts.

## Choose an exclusion

| User intent | Owning input |
| --- | --- |
| Known cache/state/data associated with a selected application | That application's `unmanaged` in shared `catalog.toml` |
| Machine-local unrelated acknowledged file/directory | Local `files.toml`, grouped under an absolute expanded parent |
| General source-native ignore, shared independently of dotm selection | Actual source `.chezmoiignore`; preserve existing rules |
| Do not select an application on this machine | Named selection's `apps`/dependency design, not `files.toml` |

Do not hide owned configuration conflicts by adding unmanaged exclusions. Other profiles' nonselected config exclusions prevent synchronization but do not acknowledge their leftover files in inventory. After an exclusion/selection edit, refresh and lint policy and inspect `files status`; these evaluate trusted templates and publish policy, but never apply files.

## First deployment prerequisites

dotm is already installed and available on PATH; do not search for a checkout, build it, or install it. Backend readiness is separate: Nix must already be a multi-user daemon with ordinary untrusted-user access, `nix-command`, `flakes`, enabled XDG base directories, and writable user Nix state. Useful diagnostics:

```sh
nix --version
nix store info
nix config show experimental-features
nix config show use-xdg-base-directories
```

Expected store: daemon, `Trusted: 0`. The session must expose `${XDG_STATE_HOME:-$HOME/.local/state}/nix/profile/bin` in PATH and its `share` in application discovery (for example XDG_DATA_DIRS). Custom XDG paths must be absolute and consistent. Git/chezmoi and Files integration are needed only for file workflows. Network/cache access is required for acquisition. Missing provisioning is an external prerequisite, not permission to use sudo or alter host services, login shell, or GUI session.

Arrange shared/local inputs per Manifests, inspect `configs`/`apps`/`show`, then `status`, and deploy only within the request. First installation locks `nixos-unstable`; later healthy installs reuse the lock. No special Nix version minimum is established by the exercised Nix 2.35.6 baseline.
