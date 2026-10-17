# Configuration reference

[Documentation](README.md) · [Русский](../ru/configuration.md)

## Shared catalog: catalog.toml

Applications are named TOML tables, not `[[apps]]` records:

```toml
[git]
nix = ["git"]
configs = ["$XDG_CONFIG_HOME/git"]

[search]
dependencies = ["git"]
nix = ["ripgrep", "fd"]

[json]
nix = ["yq-go", { attr = "jq", outputs = ["bin", "man"] }]

[shell]
dependencies = ["search"]
nix = ["zsh"]
configs = ["~/.zshrc"]
unmanaged = ["$XDG_STATE_HOME/zsh", "$XDG_CACHE_HOME/zsh"]
pre_hook = "hooks/check-home"
pre_cfg_hook = "hooks/prepare-shell"
post_cfg_hook = "hooks/check-shell"

[ssh-settings]
configs = ["~/.ssh/config"]

[workspace]
dependencies = ["shell", "json", "ssh-settings"]
```

| Field | Meaning |
| --- | --- |
| `nix` | Optional package descriptors; absent/empty means no own packages |
| `dependencies` | Other catalog application IDs, resolved transitively before consumers |
| `configs` | Owned absolute destination paths after expansion; file or directory targets |
| `unmanaged` | Known application data excluded from synchronization/inventory when selected |
| `pre_hook` | Repeatable package pre-hook executable path |
| `pre_cfg_hook` | Repeatable executable before configuration acquisition/validation/application |
| `post_cfg_hook` | Repeatable executable after successful dependency-ready configuration |

Package strings select default outputs; tables choose explicit outputs. Both forms can share an array. Attributes are relative to `nixpkgs.legacyPackages.${system}`. Package work deduplicates descriptors, so applications can share packages. Logical dependencies are user-selected companions, not technical Nix dependencies. Unknown dependencies/cycles are errors. Aggregators and configuration-only applications are valid; `post_hook` and the old `[[apps]]`/`id`/`packages` format are unsupported.

### Paths and ownership

Config/data paths expand HOME, leading `~`/`~/`, and environment variables without a shell. The XDG config/data/state/cache homes default to standard HOME locations. `${VAR:-fallback}` supports defaults, including nested expressions; unset variables without defaults are errors. Config/unmanaged paths must become absolute. Paths are cleaned and compared lexically, without requiring existence or following symlinks.

Across the **entire catalog**, applications cannot own equal/nested `configs`, and any application's `configs` cannot overlap any `unmanaged`. Redundancy within a list and overlapping unmanaged declarations warn. A directory target selects its source-managed descendants; it does not claim arbitrary unknown local files as synchronized. Local exclusions conflicting with config ownership are diagnosed by files lint.

## Shared selections: configurations.toml

```toml
[[configurations]]
name = "laptop"
apps = ["workspace"]
backend = "chezmoi"
repository = "https://example.invalid/my-dotfiles.git"

[[configurations]]
name = "packages-only"
apps = ["json"]

[[configurations]]
name = "empty"
apps = []
```

Replace the fictional repository with your real Git URL. Put `backend` and `repository` directly in each `[[configurations]]` entry alongside `name` and `apps`; the former nested `[configurations.configs]` form is rejected. Every selection whose dependency closure includes configuration targets requires the pair `backend = "chezmoi"` and a nonblank `repository`. Package-only/empty selections may omit both, but any nonempty source setting requires the valid pair. Source settings belong to each named configuration, not the local selection. All selections share one local source checkout: choosing a different URL can trigger protected source replacement. An empty set never implies deletion or unscoped apply.

## Local selection: config.toml

Location: `${XDG_CONFIG_HOME:-$HOME/.config}/dotm/config.toml`.

```toml
configuration = "laptop"
```

This file chooses an existing name. dotm neither detects the machine nor creates this file automatically. Shared input defaults to its sibling `data/`; relative `--data-dir` is resolved against the current directory and does not relocate local settings.

## Local acknowledgements: files.toml

Optional file beside `config.toml`:

```toml
[unmanaged]
"$XDG_CONFIG_HOME" = ["browser-local", "tool with spaces"]
"$XDG_CACHE_HOME" = ["manual-downloads"]
"~" = ["Downloads", "scratch"]
```

Parent keys are expanded absolute paths. Values are literal immediate child names, not globs or expandable paths: wildcard characters and `$VAR` remain literal. Empty names, `.`/`..`, slashes, and NUL are invalid. This acknowledges only those children, not the whole parent. Missing local files mean no local exclusions. Acknowledgement is not creation/deletion or proof that data is disposable. The [files workflow](chezmoi.md) explains selection-dependent filtering versus inventory classification.

## Hooks

Hook values are single executable-file paths, not command strings/argument arrays. Relative paths and working directory are based on the directory containing `catalog.toml`. Absolute, home-relative, environment, and nested-default paths are supported. No command substitution, splitting, glob expansion, or shell evaluation occurs.

Example executable script (create `hooks/check-home` yourself and set its executable bit):

```bash
#!/usr/bin/env bash
set -eu
test -d "$HOME"
```

Hooks launch directly, inherit the environment with managed profile `bin` first in PATH, have closed stdin, and send both streams to dotm's stderr. dotm never changes their permissions. A package pre-hook cannot rely on not-yet-installed packages.

Order per full cycle: **package pre-hooks → packages → eligible pre-configuration hooks → source/lint/policy/apply → post-configuration hooks**. Dependencies precede consumers, each application runs once per phase. Install pre-hooks cover every desired application even on a no-op; update package hooks cover its package-evidence/dependency scope. Configuration stages cover the full eligible desired set. `update cfg` omits package hooks/work. Inspection/removal do not run hooks.

Package pre-hook/package failures prevent configuration work. Pre-configuration failure blocks that application and consumers' configuration stages while independent work continues. Conflicts allow safe file/consumer configuration work, but block affected/consumer post-hooks. Post-hook failure blocks consumer post-hooks. Hooks repeat on retry with no completion database. Write trusted hooks to tolerate repetition/interruption: profile rollback does not undo their user-data effects.

See the [full settings showcase](../../examples/showcase/README.md) for shared descriptors and several profiles; it is not runnable as-is.
