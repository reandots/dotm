# Demo and examples

[Documentation](README.md) · [Русский](../ru/examples.md)

## Safe inspection demo: no Nix or profile changes

Build first using [installation](installation.md). Run this block from the repository root in Bash. It uses a temporary local selection rather than overwriting your real one. The trap deletes only that temporary directory. No backend, download, hook, or configuration application is involved.

```bash
(
  set -eu
  demo=$(mktemp -d)
  trap 'rm -rf -- "$demo"' EXIT
  export XDG_CONFIG_HOME="$demo/config"
  mkdir -p "$XDG_CONFIG_HOME/dotm"
  printf 'configuration = "laptop"\n' > "$XDG_CONFIG_HOME/dotm/config.toml"

  ./dotm --data-dir examples configs
  ./dotm --data-dir examples apps
  ./dotm --data-dir examples show

  printf 'configuration = "minimal"\n' > "$XDG_CONFIG_HOME/dotm/config.toml"
  ./dotm --data-dir examples show
)
```

The [bundled catalog](../../examples/catalog.toml) and [selections](../../examples/configurations.toml) are package-only:

| Selection | Top-level apps | Resolved dependency-first order |
| --- | --- | --- |
| `laptop` | `terminal`, `git`, `json` | `zoxide`, `git`, `zsh`, `search`, `terminal`, `json` |
| `minimal` | `git`, `search` | `git`, `search` |

Packages include zsh, zoxide, git, ripgrep, fd, eza, and jq; jq explicitly selects `bin` and `man`. `show` prints deduplicated package descriptors without evaluating nixpkgs. This is a manifest demo, not proof of host readiness.

## Real package workflow

The following commands are **not part of the safe demo**. They use real local settings and the standard XDG Nix profile. Complete [host preparation](installation.md), create the local selection deliberately, and inspect before mutation:

```sh
./dotm --data-dir examples show
./dotm --data-dir examples status
./dotm --data-dir examples install
./dotm --data-dir examples update
```

To exercise switching, edit your selection to `minimal`, inspect status, then review `remove-extras`. Select `laptop` and install to restore missing applications using the current lock. Cleanup/GC is a separate explicit operation. Do not assume changing only XDG_CONFIG_HOME isolates package state: the native profile follows XDG_STATE_HOME/HOME.

## Full static settings showcase

[examples/showcase](../../examples/showcase/README.md) demonstrates application aggregators, configuration-only apps, shared descriptors, explicit outputs, owned files/directories, unmanaged data, all three hooks, several named profiles, and local settings.

It is **not runnable**: `.invalid` repository URLs are fictional; hook implementations and source contents are absent. Its `local/` tree represents files under a sample user's config home, not files dotm automatically discovers beside a catalog. Replace URLs, implement hooks, and provision paths before deriving a real setup. Switching profiles does not itself delete packages or destination files.

## File-policy integration example

[examples/chezmoi-policy](../../examples/chezmoi-policy/) supplies the root inclusion, illustrative static config, and nested native-ignore fixture. Adopt the template in your owning dotfiles repository while retaining native rules; do not overwrite live configuration with unresolved placeholders. Follow the [integration recipe](chezmoi.md), including the calculated policy-file binding.

## Reproducing backend acceptance

Historical isolated real-backend scenarios and their environment/protection boundaries are recorded separately:

- [Package acceptance](../../.agent/acceptance.md): Arch and disposable Ubuntu, ordinary multi-user-daemon user, rollback and explicit GC.
- [Configuration/files acceptance](../../.agent/configuration-acceptance.md): isolated HOME/XDG state, Git/chezmoi and Arch Nix lifecycle composition.

These are maintainer verification records, not commands to replay against your live profile. See [coverage limits](troubleshooting.md).
