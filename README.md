# dotm

Deploy personal Linux application environments with **Nix** and synchronize application configurations with **chezmoi**, without Home Manager. Host provisioning stays with **Ansible**.

**[English documentation](doc/en/README.md) · [Документация на русском](doc/ru/README.md)**

## What it does

- Resolves named configurations and transitive application dependencies from TOML manifests.
- Installs missing packages, explicitly updates installed desired applications, and removes extras only with approval.
- Applies eligible application configurations without forcing overwrites of local conflicts.
- Runs repeatable application hooks and checks file filtering and unclassified HOME/XDG paths.
- Diagnoses dotm inputs, backend/host prerequisites, and current-session paths with read-only `doctor`.
- Separates profile-history cleanup from explicitly requested shared-store garbage collection.

dotm is a personal Linux CLI, not a system provisioner. It requires a configured multi-user Nix daemon; configuration deployment additionally requires Git. `dotm init` prepares chezmoi/source/policy integration; if chezmoi is missing, it adds/selects an ordinary catalog application and installs only it and its dependencies. It does not install dotm itself or Nix/Git, change the login shell, configure a desktop session, or enable system services.

## Start here

```sh
# Build from a checkout with Go available:
go build .
# Inspect the bundled package-only catalog; no Nix operation or local selection:
./dotm --data-dir examples configs
./dotm --data-dir examples apps
```

For pinned developer tools, use `just setup`; this trusts the checkout's mise configuration and downloads tools and dependencies. See [development](doc/en/development.md).

Before deploying, follow [installation and host preparation](doc/en/installation.md), create your [manifests and local selection](doc/en/configuration.md), then review `show` and `status`. `install` and `update` can run trusted hooks and modify application files, not just packages.

Mutating package reports describe effects observed during this invocation, not the full installed inventory; use `-v` / `--verbose` for unchanged inventory and backend detail. Configuration reports summarize the overall result, with applied paths/source changes only when reported; a completed config cycle or hooks do not imply changed files. Warnings, failures, blockers, backups, partial effects, and actual native/hook output remain visible. Status, show, list, and check commands continue to report the state or findings requested. Terminal colors respect `NO_COLOR` and disappear when redirected. After `init` edits the checkout integration, review and commit its source changes before ordinary synchronization.

For an existing installation, follow the [integration transition guide](doc/en/chezmoi.md#upgrade-an-existing-installation): explicitly adopt the root block with `init`, review/publish the source diff, then diagnose before deployment. Ordinary lifecycle/refresh does not migrate integration; doctor does not repair it.


## Documentation

| Topic | English | Русский |
| --- | --- | --- |
| Overview and reading guide | [Overview](doc/en/README.md) | [Обзор](doc/ru/README.md) |
| Installation and host prerequisites | [Installation](doc/en/installation.md) | [Установка](doc/ru/installation.md) |
| Commands and daily workflows | [Usage](doc/en/usage.md) | [Использование](doc/ru/usage.md) |
| TOML formats, paths, and hooks | [Configuration](doc/en/configuration.md) | [Настройка](doc/ru/configuration.md) |
| chezmoi, file policy, and inventory | [Configuration files](doc/en/chezmoi.md) | [Конфигурационные файлы](doc/ru/chezmoi.md) |
| Safe demo and examples | [Examples](doc/en/examples.md) | [Демо и примеры](doc/ru/examples.md) |
| Failures, recovery, and limits | [Troubleshooting](doc/en/troubleshooting.md) | [Устранение проблем](doc/ru/troubleshooting.md) |
| Tooling, architecture, and checks | [Development](doc/en/development.md) | [Разработка](doc/ru/development.md) |

For AI-assisted end-user operations, use the self-contained [dotm user skill](doc/skills/dotm-user/SKILL.md): application manifests, selections, exclusions, CLI workflows, and recovery, without development rules.

Maintainer contracts and verification records live in the English [knowledge base](.agent/README.md). Public documentation describes supported behavior; acceptance records distinguish exercised environments from unverified ones.
