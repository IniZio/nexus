# Operations playbook

Short reference for **latency**, **isolation concepts**, and **paths**.

## Doctor vs backend

- **`nexus doctor`** requires `--project-root` as a **single absolute path** and `--suite <name>`. There is no top-level `--timeout` on `doctor` (unlike `nexus exec`); probes use internal timeouts.
- On startup, the CLI prints **`doctor: runtime backend=…`** so you know whether you are on **firecracker** or **seatbelt** (or other supported backend).
- **Firecracker, cold VM:** the first run can take **several minutes** (guest bootstrap, Docker/tooling) before your `.nexus/probe` scripts run. Silence is often normal.
- **Seatbelt** (common macOS fallback when nested virtualization is unavailable): usually **much faster** for the same project.
- Predicting backend: see `.nexus/run/nexus-init-env` after `nexus init`, `nexus create --backend …`, and host capabilities. See [Workspace config](../reference/workspace-config.md).

## Isolation: fork vs workspace vs git worktree

| Mechanism | What it isolates | Typical use |
|-----------|------------------|-------------|
| **Git worktree** | Second checkout + branch on the **same machine** | Parallel features without branch switching in one tree. |
| **New Nexus workspace (`create`)** | Separate workspace id, runtime, and often VM | Remote execution, different repos/refs, clean processes. |
| **`fork`** | Child workspace derived from a parent (product semantics) | Experiment from a snapshot; check current docs for auth-bundle and metadata. |

Worktrees do **not** replace Nexus workspaces for remote sandboxes; they solve different problems.

## Absolute `--project-root`**

`nexus exec` and `nexus doctor` reject relative `--project-root`. On error, the CLI suggests a resolved absolute path. Wrappers such as `$(pwd -P)` or a small shell script (see `.case-studies/ledgerlens-demo/scripts/doctor.sh`) avoid mistakes.

## Related

- [Host auth bundle](../reference/host-auth-bundle.md)
- [CLI reference](../reference/cli.md)
- [Installation](installation.md)
