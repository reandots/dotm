# User manifests

## Locate the inputs

| Input | Default location | Purpose |
| --- | --- | --- |
| Shared catalog | `${XDG_CONFIG_HOME:-$HOME/.config}/dotm/data/catalog.toml` | Application definitions |
| Shared selections | Same directory, `configurations.toml` | Named machine/use-case selections |
| Local selection | `${XDG_CONFIG_HOME:-$HOME/.config}/dotm/config.toml` | Select one existing name |
| Optional local exclusions | Beside local selection, `files.toml` | Machine-local acknowledgements |
| Optional hook executables | Usually shared `hooks/` | Referenced by catalog paths |

`--data-dir /path/to/data` overrides only the shared directory, including the base for relative hook paths. Relative overrides use the current working directory. No automatic current-directory discovery, manifest download, or machine detection exists. Shared data may be a Git checkout or a symlink to one. Keep local files outside shared data unless explicitly arranging separate deployment yourself.

## catalog.toml

Each top-level named table is an application ID. This complete example demonstrates independent supported fields; do not copy unrelated settings into a new entry:

```toml
[git]
nix = ["git"]
configs = ["$XDG_CONFIG_HOME/git"]

[search]
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
dependencies = ["git", "shell", "json", "ssh-settings"]
```

All application fields are optional. An empty application/aggregator or a configuration-only application is valid.

| Field | Value and effect |
| --- | --- |
| `nix` | Array of package strings or tables with `attr` and explicit `outputs`; absent/empty means no own packages |
| `dependencies` | Array of other catalog IDs; selects their complete transitive closure |
| `configs` | Array of owned destination file/directory paths, absolute after expansion |
| `unmanaged` | Array of known application-data paths, absolute after expansion; active for selected applications |
| `pre_hook` | Single executable path before package work |
| `pre_cfg_hook` | Single executable path before eligible configuration work |
| `post_cfg_hook` | Single executable path after successful dependency-ready configuration |

Package strings request default outputs; tables request specified outputs. Attribute names are relative to `nixpkgs.legacyPackages.${system}`, not executable names or arbitrary flake URLs. Shared descriptors are deduplicated. Logical dependencies express companions the user wants, not Nix technical build/runtime dependencies. Unknown references and cycles fail. Do not use the unsupported `[[apps]]`/`id`/`packages` layout or `post_hook`.

### Expansion and ownership

Paths support HOME, `~`/`~/`, environment variables, and `${VAR:-fallback}` including nested defaults, without shell execution. Missing variables without defaults fail. XDG config/data/state/cache homes default to standard HOME locations. Config/unmanaged paths must become absolute; normalization is lexical, does not require existence, and does not follow symlinks.

Across the **whole catalog**, equal/nested `configs` ownership between applications is invalid, even across different selections. Any config/unmanaged overlap is invalid. Redundant list paths and overlapping unmanaged declarations warn. A declared directory scopes source-managed descendants, not arbitrary unknown local siblings. Local exclusions conflicting with owned configs are diagnosed by `files lint`.

## configurations.toml

```toml
[[configurations]]
name = "laptop"
apps = ["workspace"]
[configurations.configs]
backend = "chezmoi"
repository = "https://example.invalid/my-dotfiles.git"

[[configurations]]
name = "packages-only"
apps = ["search"]

[[configurations]]
name = "empty"
apps = []
```

Use a unique name and an explicit application list (which may be empty). Application IDs must exist. If the selection's dependency closure includes any `configs` targets, its `configs` settings must specify `backend = "chezmoi"` and a nonempty real Git repository URL. Do not invent a URL. Package-only/empty selections need no backend settings. Settings are per named configuration, not in local `config.toml`. All configurations share one local source checkout, so URL switches can require protected replacement. Empty selections never request deletion or unscoped file application.

## Local config.toml

```toml
configuration = "laptop"
```

The name must already exist in shared selections. Edit this setting to switch machines/use cases; selection alone neither installs nor removes packages/files.

## Optional local files.toml

```toml
[unmanaged]
"$XDG_CONFIG_HOME" = ["browser-local", "tool with spaces"]
"$XDG_CACHE_HOME" = ["manual-downloads"]
"~" = ["Downloads", "scratch"]
```

Keys expand to absolute parent paths. Values are literal immediate child names, not paths, globs, or variable expressions: `$VAR` and wildcard characters remain literal. Empty names, `.`, `..`, slashes, and NUL are invalid. An entry acknowledges that child/subtree, not every child of the parent. Missing `files.toml` means no local exclusions. Acknowledgement does not create/delete anything or mean the data is disposable.

## Hooks

Use only when the user needs repeatable custom actions. Values are executable-file paths, not shell command strings or argument arrays. Relative paths and working directory use the catalog directory. Absolute/home/environment/default forms work. No splitting, globbing, substitution, or shell evaluation occurs. Provision executable permission yourself; dotm never changes permissions.

Hooks execute directly with inherited environment, managed profile `bin` first in PATH, closed stdin, and both streams on stderr. A package pre-hook cannot depend on packages not yet installed.

Full cycle order: package pre-hooks → packages → eligible pre-configuration hooks → source/lint/policy/apply → post-configuration hooks. Dependencies run before consumers, once per application per phase. Install package hooks cover all desired applications even on no-op. Update package hooks follow active package evidence/dependency scope; configuration phases cover all eligible desired applications. `update cfg` omits package hooks/work. Inspection and removal run no hooks.

Package/pre-hook failure stops configuration work. A pre-configuration failure blocks that application and its consumers, not independent work. Conflicts preserve files and block affected/consumer post-hooks; post-hook failure blocks consumer post-hooks. Hooks repeat after retries with no completion database. They must tolerate repetition/interruption; package rollback cannot undo user-data effects. One-time/removal hooks are not supported.
