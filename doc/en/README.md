# dotm documentation

[Project introduction](../../README.md) · [Русский](../ru/README.md)

## Purpose and boundaries

dotm deploys a personal application environment across Linux distributions. Nix manages packages in the standard XDG user profile; chezmoi manages selected application files. No Home Manager is involved. Ansible remains responsible for OS provisioning, Nix installation, host settings, services, and session integration.

An **application** is a logical catalog entry: it may contain several packages, configuration targets, hooks, or only dependencies. A **configuration** is a named selection for a machine or use case, not a Linux distribution. Selecting an application selects all its transitive logical companions; Nix handles technical package dependencies.

## Reading guide

1. [Installation](installation.md): build dotm, prepare Nix and session paths, arrange local settings, and run `dotm init`.
2. [Demo and examples](examples.md): inspect the example catalog without touching a profile.
3. [Configuration reference](configuration.md): write shared TOML manifests, local selection, exclusions, and hooks.
4. [Usage](usage.md): inspect, install, update, switch configurations, remove extras, and clean history.
5. [chezmoi and files](chezmoi.md): adopt filtering integration, [upgrade an existing installation](chezmoi.md#upgrade-an-existing-installation), synchronize safely, inspect file changes.
6. [Troubleshooting](troubleshooting.md): read-only `doctor`, exit codes, conflicts, recovery, and coverage limits.
7. [Development](development.md): pinned tools, package structure, conventions, and checks.

For AI-assisted operation, use the packaged [dotm user skill](../skills/dotm-user/SKILL.md). Its entrypoint routes to self-contained references for manifests, application additions, CLI commands, exclusions, chezmoi, verification, and recovery; it does not cover developing dotm. A complete Russian entrypoint is stored at `doc/skills/dotm-user/translations/SKILL.ru.md`; it can replace the package's `SKILL.md` while retaining the same resource paths.

## Safety model

Inspection is separate from mutation. `install` never removes extras; `update` does not install wholly absent independent applications. Removal and cleanup require approval. Garbage collection is never implicit and can affect the whole shared Nix store.

Package updates have recovery machinery, but this is not a transaction over hooks or user data. Configurations can partially succeed while conflicts preserve local files. Hooks and chezmoi file templates are trusted code, not sandboxed input. Read each operation's documented boundaries before running it against real state.

The [maintainer knowledge base](../../.agent/README.md) records detailed contracts and historical verification. Public documentation in English and Russian is kept equivalent; examples with fictional repositories are not runnable deployment presets.
