# Installation and first deployment

[Documentation](README.md) · [Русский](../ru/installation.md)

## Build the CLI

From a trusted checkout with Go installed:

```sh
go build .
./dotm --help
```

This creates `./dotm`; it does not install it into PATH. Examples in this guide assume commands run from the checkout. You can place the binary on your user PATH using your provisioning/configuration tooling.

The module declares Go 1.23.0. Build and CLI checks were exercised with Go 1.27.4 on Linux/amd64; older toolchains have not been tested. Downloads require network access and writable Go caches. For pinned tooling and `just setup`, see [development](development.md). No public automatic installer or setup/bootstrap command is available.

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

For configuration deployment, additionally provide **Git and chezmoi** in advance and complete [chezmoi integration](chezmoi.md). Ordinary commands do not bootstrap a missing backend. An internal component for future setup can reuse compatible chezmoi or acquire the canonical nixpkgs `chezmoi` descriptor through the same generated source/lock and standard XDG profile, without installing selected applications. It checks actual required operations and formats, not an invented minimum version; an existing unusable candidate must be repaired rather than silently shadowed. This is not a public `init` command or a dotm self-installer.

A backend acquired by that component is recorded separately in machine-local `dotm/infrastructure.json` under XDG state and shown as infrastructure in `status`. It stays protected from `remove-extras`, including `--yes`, even after a catalog application sharing its descriptor is dropped. Ordinary installs/repeated bootstrap keep the lock; ordinary updates advance that backend only when its descriptor belongs to the selected application update scope. External executables are not claimed. Session PATH/share provisioning remains separate.

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

`data/` may be a Git checkout or a symlink to one. dotm does not create/download missing manifests. `--data-dir` changes only shared inputs, not local settings; there is no current-directory discovery. See [formats](configuration.md).

For a new setup, create `config.toml` containing:

```toml
configuration = "laptop"
```

Use a name declared in your `configurations.toml`. Edit an existing file rather than overwriting it. The package-only example catalog can be selected with `--data-dir examples`; a safe isolated inspection recipe is in [examples](examples.md).

## Review, then deploy

```sh
./dotm configs
./dotm apps
./dotm show
./dotm status
# Mutates packages and may execute hooks/apply configuration files:
./dotm install
./dotm status
```

`configs`, `apps`, and `show` do not need Nix. `status` inspects the native profile. First installation locks `github:NixOS/nixpkgs/nixos-unstable`; subsequent healthy installations reuse that revision. Use `update` explicitly to advance it. Review [usage](usage.md) and [recovery boundaries](troubleshooting.md) before deploying your own catalog.
