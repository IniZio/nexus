# E2E Migration Manifest

This document tracks migration from legacy runtime smoke scripts to the consolidated SDK runtime e2e package.

## Mapping

| Legacy script | New suite / case IDs | Status |
| --- | --- | --- |
| `scripts/ci/pty-runtime-e2e.sh` | `runtime-selection/*`, `worktree-sync/host-to-workspace-file-propagation`, `spotlight-compose/apply-compose-ports-list-close`, `tools-auth-forwarding/mint-exec-revoke` | migrated |
| `scripts/ci/pty-lxc-managed-e2e.sh` | `runtime-selection/unsupported-nested-virt-seatbelt`, `spotlight-compose/apply-compose-ports-list-close` | migrated |
| `scripts/ci/nexus-subcommand-e2e-init-exec.sh` | `runtime-selection/*`, `lifecycle-hooks/prestart-poststart-prestop-order` | migrated |
| `scripts/ci/nexus-subcommand-e2e-doctor-backends.sh` | `runtime-selection/*`, `ui-cli-parity-map` | migrated |
| `packages/nexus/scripts/pty-remote-smoke.js` | `tools-auth-forwarding/mint-exec-revoke`, `worktree-sync/host-to-workspace-file-propagation` | migrated |

## Consolidated entrypoint

- Package: `@nexus/e2e-sdk-runtime`
- Command: `pnpm --filter @nexus/e2e-sdk-runtime test:ci`
