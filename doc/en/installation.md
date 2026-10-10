# Installation and first deployment

[Documentation](README.md) · [Русский](../ru/installation.md)

## Build the CLI

From a trusted checkout with Go installed:

```sh
go build .
./dotm --help
```

This creates `./dotm`; it does not install it into PATH. Examples in this guide assume commands run from the checkout. You can place the binary on your user PATH using your provisioning/configuration tooling.

The module declares Go 1.23.0. Build and CLI checks were exercised with Go 1.27.4 on Linux/amd64; older toolchains have not been tested. Downloads require network access and writable Go caches. For pinned tooling and `just setup`, see [development](development.md). There is no documented automatic installer or backend bootstrap.

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

For configuration deployment, additionally provide **Git and chezmoi** in advance and complete [chezmoi integration](chezmoi.md). Automatic backend installation is not implemented.

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
