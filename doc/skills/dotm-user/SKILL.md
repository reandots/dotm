---
name: dotm-user
description: "Manage a user's dotm application environment: add applications and Nix packages, edit catalog.toml and configurations.toml, select machine configurations, configure file exclusions and chezmoi integration, inspect, install, update, remove extras, and diagnose user-facing failures. Use for operating dotm and maintaining its user manifests, not for developing dotm, changing its implementation, or provisioning the host."
---

# Operate dotm

Use this skill to change the user's environment through dotm's supported inputs and commands. Do not read application source code or maintainer KB documents to complete ordinary user tasks. The packaged references contain the operating contract; consult the installed CLI's `--help` for syntax differences and report unsupported behavior rather than inventing options.

## Choose the reference

- Before editing any TOML, read [Manifests](references/manifests.md): locations, required versus optional settings, package descriptors, dependencies, paths, ownership, and hooks.
- To add an application, select a profile, or prepare a first deployment, read [Workflows](references/workflows.md).
- Before running commands, read [CLI](references/cli.md): inspection versus mutation, update scope, removal, cleanup, combined flags, and verification.
- For configuration targets, exclusions, chezmoi setup, or file inventory, read [Files](references/files.md). It includes the policy integration template.
- On failure, conflict, interruption, or recovery requests, read [Recovery](references/recovery.md).

## Execution contract

1. Assume `dotm` is already installed and available on PATH; invoke it as `dotm`, without locating a checkout, building, installing, or substituting `./dotm`. Locate the shared data directory and machine-local selection. Read existing user manifests/settings before editing. `--data-dir` moves only shared inputs; never assume current-directory discovery or overwrite local settings with a template.
2. Resolve the selected named configuration and existing dependency closure. Preserve unrelated applications, selections, native ignore rules, and user data. For a requested application, reuse an existing catalog entry or selected aggregator where appropriate; a catalog entry alone does not select it. Ask which selection to change only when the user's scope and existing selection cannot answer.
3. Make the smallest user-input change using the relevant reference. Distinguish application IDs from Nix attributes and command names. Do not invent configuration targets, hooks, backend sources, or technical package dependencies merely because a package is added.
4. Verify declarations through `configs`, `apps`, and `show` with the actual data directory/local selection. Use `status` when Nix is available. For file changes, follow the separate passive lint, policy refresh, and inventory gates in the Files reference. A parser check alone is insufficient.
5. If the request is only to edit or explain inputs, propose the next mutating command instead of deploying. If deployment is requested, review the resolved scope and execute within the user's authorization. `install` and `update` may run trusted hooks and apply files; neither is a dry run. Never add `--yes`, shared-store GC, source replacement, or forceful data reconciliation without that scope being explicit.
6. Report changed inputs, checks actually run, unresolved warnings/conflicts, and the next command if not executed. Never call package readiness proof of synchronized files or successful lint proof of template safety.

Host provisioning, Nix/backend installation, shell/session/service setup, and dotm development are outside this skill. Identify the external prerequisite instead of silently expanding the task.
