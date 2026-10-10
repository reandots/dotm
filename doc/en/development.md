# Development

[English index](README.md) · [Русская версия](../ru/development.md)

This guide covers the project toolchain, architecture, and contribution workflow. For contributor-specific rules and detailed lint/test conventions, see the [development standards in the knowledge base](../../.agent/development.md). User-facing setup and behavior are documented in [installation](installation.md), [usage](usage.md), [configuration](configuration.md), and [chezmoi integration](chezmoi.md).

## Prepare a checkout

Install [mise](https://mise.jdx.dev/) and [just](https://just.systems/), then from the repository root run:

```sh
just setup
```

This recipe explicitly trusts this checkout's `mise.toml`, installs the pinned development tools, and downloads Go module dependencies with the pinned Go toolchain. **Trust warning:** only run it for a checkout whose configuration you trust; `mise trust mise.toml` authorizes that project's tool configuration. Setup is safe to repeat and stops on the first failure. It does not activate mise in your shell or run project checks. Installation needs network access and writable mise data/state/cache directories; dependency downloads need network access and writable Go caches. If a sandbox blocks setup writes, run setup outside the sandbox or grant the necessary filesystem access.

`mise.toml` pins Go 1.27.4, golangci-lint 2.15.0, gopls v0.24.0, and Delve 1.27.2. The application module separately declares `go 1.23.0` in `go.mod`; this is the module's language/toolchain compatibility baseline, not the project-selected development Go version. Use the pinned Go version for project commands. With mise activated, invoke `just <recipe>`; otherwise use `mise exec -- just <recipe>` to avoid accidentally selecting unrelated global tool versions.

## Editor tools

gopls and Delve support editor navigation and debugging; they are not additional precommit checks. For VS Code, expose mise shims or use the [mise VS Code integration](https://mise.jdx.dev/ide-integration.html#vs-code) for the Go extension, then run **Go: Locate Configured Go Tools** to check which tools it selected. Restart affected tools after changing versions. This repository does not override VS Code user settings.

## Recipes

Run `just` to list the available commands.

| Recipe | Purpose |
| --- | --- |
| `just setup` | Trust the checkout's mise config, install pinned tools, and download Go dependencies. |
| `just fmt` | Format Go sources in place using the configured gofumpt formatter. |
| `just fmt-check` | Check formatting without editing sources; print a diff on failure. |
| `just vet` | Run `go vet ./...`. |
| `just lint` | Run pinned golangci-lint checks without rewriting sources or module files. |
| `just lint-fix` | Apply supported linter fixes in place. |
| `just test` | Run all Go tests with `go test ./...`. |
| `just mod-check` | Check module tidiness without rewriting `go.mod` or `go.sum`. |
| `just build` | Build the local `./dotm` binary; do not install it. |
| `just build-release` | Build the same `./dotm` path with `-ldflags="-s -w"`, omitting debugger symbols and DWARF information. |
| `just run --data-dir examples apps` | Run the CLI, forwarding arguments after the recipe name. |
| `just precommit` | Run `lint`, `test`, then `build`, stopping on failure. |

Formatting and lint recipes call golangci-lint directly and share `.golangci.yml` settings, including gofumpt, a 120-character line limit, and explicitly selected govet analyzers. Checks cover tests as well as production code, and module downloads use read-only mode. `fmt-check` and `lint` do not change sources; `fmt` and `lint-fix` do. `precommit` does not run `fmt`, `vet`, or `mod-check`, install a Git hook, or rewrite sources/module files. Run `just mod-check` separately when appropriate. For intentional dependency changes, run `go mod tidy` explicitly and include the resulting module-file changes. The release build is for a smaller binary when symbol-based debugging is not needed; normal development and precommit use `just build`.

## Architecture

The executable entry point is `main.go`: it delegates arguments and standard streams to `internal/cli.Run`, reports errors, and maps them to CLI exit codes. The `internal/` packages group the application's responsibilities:

- `cli` — command parsing and orchestration of user-facing operations.
- `manifest` — reading and resolving manifest file definitions.
- `installed` — installed-state configuration, source state, install/update/remove/clean operations, and recovery.
- `chezmoi` — chezmoi source inventory, policy, validation, and apply behavior.
- `hooks` — shared hook path and hook-related support.
- `pathutil` — path utilities shared across the implementation.

Keep reusable domain behavior in the relevant internal package and CLI-specific argument/command handling in `internal/cli`. Examples under `examples/` provide sample catalogs and configurations; they are not a substitute for tests.

## Tests and contribution conventions

Tests live beside the package they exercise. Follow the existing `testify/suite` pattern: a suite embeds `suite.Suite`, a top-level `Test…Suite(t *testing.T)` calls `suite.Run`, and table-driven cases use suite subtests. Use `s.Require()` for prerequisites whose failure means a test cannot safely continue, and ordinary suite assertions for independent outcomes. Focus on complex logic, error-prone boundaries, and meaningful regressions rather than trivial details or a coverage target.

Some installed-source recovery tests use isolated fake-Nix executables implemented in Python 3. Ensure `python3` is on `PATH` when running `go test ./internal/installed` or the full suite. These fixtures do not mutate the real Nix profile or store. Executable test fixtures should use owner-only permissions and documented local linter exceptions where required; see the [knowledge-base standards](../../.agent/development.md) for exact rules. Keep linting and formatting enabled for tests and generated files; do not add blanket exclusions.

Before submitting, run the checks relevant to your change and normally `just precommit`; separately consider `just fmt-check` and `just mod-check` since they are not part of that recipe. Do not claim checks you did not run. When a behavior, command, setup requirement, or configuration changes, update the applicable public documentation in **both** `doc/en/` and `doc/ru/`, and keep the root README's concise English overview and links current. Keep the two language guides equivalent in coverage. The [knowledge base](../../.agent/development.md) is for contributor-specific detail; public behavior belongs in the public docs.
