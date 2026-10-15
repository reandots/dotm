# Installation and first deployment

[Documentation](README.md) · [Русский](../ru/installation.md)

## Build the CLI

From a trusted checkout with Go installed:

```sh
go build .
./dotm --help
```

This creates `./dotm`; it does not install it into PATH. Examples in this guide assume commands run from the checkout. You can place the binary on your user PATH using your provisioning/configuration tooling.

The module declares Go 1.23.0. Build and CLI checks were exercised with Go 1.27.4 on Linux/amd64; older toolchains have not been tested. Downloads require network access and writable Go caches. For pinned tooling and `just setup`, see [development](development.md). `dotm init` prepares an already running dotm; it does not install the dotm executable itself.

## Prepare the host separately

Application operations require an already configured **multi-user Nix daemon**. dotm does not install or update Nix. No Home Manager, sudo, or trusted-user privileges are required. Nix 2.35.6 is an exercised baseline, not a proven minimum.

Run as the application user:

```sh
nix --version
nix store info
nix config show experimental-features
nix config show use-xdg-base-directories
```

Expected: daemon store, `Trusted: 0`, both `nix-command` and `flakes`, and enabled XDG base directories. Provision the writable `${XDG_STATE_HOME:-$HOME/.local/state}/nix` directory for this user. Keep custom XDG values absolute and consistent between provisioning and the login session. Package/source downloads also need network access and writable Nix caches/state.

Host/session automation must expose:

- `${XDG_STATE_HOME:-$HOME/.local/state}/nix/profile/bin` through `PATH`;
- the profile's `share` directory through application discovery, such as `XDG_DATA_DIRS`.

Start a new session after changing session setup. dotm does not alter the invoking shell, change the login shell, enable services, or configure a graphical session. Hooks prepend the profile's `bin` only to their own PATH. The exercised Arch provisioning details are in [Nix host preparation](../../.agent/nix-host.md).

For configuration deployment, Git must be available. `dotm init` reuses a compatible existing chezmoi. If absent, it adds ordinary application `chezmoi` to `catalog.toml` when needed, includes it in the selected configuration in `configurations.toml` unless already selected transitively, and installs only that application and its dependencies. Existing custom declarations and unrelated TOML bytes/comments are preserved. Required operations/formats are probed; an unusable existing executable fails rather than being silently shadowed. Nix/Git installation and host/session provisioning remain external. Compatible external executables are reused without automatic selection/adoption.

## Arrange inputs

Default layout:

```text
${XDG_CONFIG_HOME:-$HOME/.config}/dotm/
  config.toml              # machine-local selection
  files.toml               # optional local file acknowledgements
  data/
    catalog.toml           # shared application catalog
    configurations.toml    # shared named configurations
    hooks/                 # optional executable hooks
```

`data/` may be a Git checkout or a symlink to one. Ordinary commands do not create or download missing manifests; `init` can create missing dotm input files as part of an approved setup, but does not download remote shared manifests. `--data-dir` changes only shared inputs, not local settings; there is no current-directory discovery. See [formats](configuration.md).

For a new setup, init can prepare local selection settings after reviewing its plan; you do not need to create `config.toml` first if you pass `--configuration` or existing inputs determine it unambiguously. Init validates/creates only the dotm files needed by the approved setup and does not invent a configuration when its name is unresolved. For example, if your `configurations.toml` declares `laptop`, init can prepare the local selection for it. Edit existing files rather than overwriting them.

If you prefer to prepare the local selection manually, its minimal form is:
```toml
configuration = "laptop"
```

Use a name declared in your `configurations.toml`. Edit an existing file rather than overwriting it. The package-only example catalog can be selected with `--data-dir examples`; a safe isolated inspection recipe is in [examples](examples.md).

## Initialize dotm

After building or otherwise installing the dotm executable and preparing Nix/Git as described above, use `init` to prepare its files backend and policy integration:

```sh
./dotm init --configuration laptop --repository https://example.invalid/user/dotfiles.git
# For non-default shared manifests (global option precedes the command):
./dotm --data-dir /path/to/data init --configuration laptop --repository https://example.invalid/user/dotfiles.git
```

`--configuration` selects the named configuration and `--repository` supplies the chezmoi source. Omit either only when existing local inputs provide an unambiguous value. `--data-dir` is the global option for shared catalog/configuration manifests; it does not move machine-local settings. Init gathers and validates available inputs and target paths, then asks for independent approvals of local-intent changes and, when applicable, source replacement. There is no `--yes` bypass; cancellation or incomplete input stops setup before the corresponding writes.

Init creates or updates approved dotm files, obtains a usable chezmoi if needed, validates or acquires the configured checkout, prepares compatible static settings, installs dotm's owned `.chezmoiignore` integration, publishes the initial policy, and verifies normal discovery. On a matching existing checkout it validates locally without fetching or fast-forwarding. It preserves unrelated dirty source content and native ignore rules; unsafe, ambiguous, partially marked, or user-modified integrations require manual correction. Compatible existing chezmoi settings are preserved; minimal static TOML is created only when no normal configuration is selected.

Init does not install the rest of the selected applications, run configuration hooks, apply destination files, commit changes, or silently switch origin. Acquisition runs ordinary package pre-hooks for chezmoi and its dependencies; those hooks cannot assume a first-time package is already installed. Checkout acquisition/replacement follows strict safeguards. Reported manifest/package/source effects and backups remain after later failures; review and commit shared-manifest and checkout changes yourself. Successful repeats make no unnecessary changes. See [usage](usage.md#initialize-dotm) and [chezmoi integration](chezmoi.md).

Chezmoi follows ordinary selected-application update/removal rules, without infrastructure protection. Old `dotm/infrastructure.json` files are ignored and not deleted automatically. Install/update resolve and verify the current profile backend after package work, using the new version even without profile PATH exposure. Missing session exposure still needs external correction. An incompatible updated backend fails configuration work without undoing the completed package update.

## Review, then deploy

```sh
./dotm doctor
./dotm configs
./dotm apps
./dotm show
./dotm status
# Mutates packages and may execute hooks/apply configuration files:
./dotm install
./dotm status
```

`configs`, `apps`, and `show` do not need Nix. `status` inspects the native profile. First installation locks `github:NixOS/nixpkgs/nixos-unstable`; subsequent healthy installations reuse that revision. Use `update` explicitly to advance it. Review [usage](usage.md) and [recovery boundaries](troubleshooting.md) before deploying your own catalog.
