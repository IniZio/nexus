# LedgerLens Demo

Minimal **case study** project for Nexus: three parallel workstreams that justify isolation (separate branches, git worktrees, or Nexus workspaces) without shipping real product code.

## Scenario

**LedgerLens** is a fake product that ingests bank CSV exports and shows a read-only dashboard.

| Workstream | Focus | Suggested branch prefix |
|------------|--------|-------------------------|
| **API** | Validation rules, CSV ingest HTTP API | `feat/ledgerlens-rules-api` |
| **UI** | Dashboard shell and charts | `feat/ledgerlens-ui` |
| **Infra** | Compose, probes, doctor suites | `feat/ledgerlens-infra` |

**Done** for this folder means: repo metadata under `.nexus/` matches `nexus init` conventions, and a developer can follow the commands below against a running Nexus daemon (remote or local).

## Fast path (no daemon)

Validate the scaffold and scripts without Nexus:

```bash
npm run check
```

This does not exercise workspaces, tunnels, or `doctor`.

## Prerequisites

- Nexus CLI installed and configured to talk to your **workspace daemon** (daemon may run on another machine than the CLI).
- For **`nexus exec`** and **`nexus doctor`**, `--project-root` must be a **single absolute path**. Relative paths and subshells that do not resolve to an absolute path are rejected. Use one of:
  - **`npm run doctor`** from this directory (runs `scripts/doctor.sh`, which resolves the repo root with `pwd -P`).
  - **`export LEDGERLENS_ROOT=$(pwd -P)`** then `nexus doctor --project-root "$LEDGERLENS_ROOT" --suite local`.
  - Inline: `nexus doctor --project-root "$(pwd -P)" --suite local` (must be executed from this directory, or substitute a real absolute path).

## Initialize project metadata

From this directory:

```bash
nexus init --force
```

Or pass a path explicitly (positional or `--project-root`); the CLI resolves it to an absolute path before writing `.nexus/`. `--force` overwrites generated `.nexus` files when you are refreshing the scaffold.

## Create a remote workspace

```bash
cd /path/to/ledgerlens-demo
nexus create
nexus list
nexus start <workspace-id>
```

Use the printed `<workspace-id>` with `ssh`, `tunnel`, `stop`, and `remove`.

**Before first `doctor` on Firecracker:** start the workspace once (`nexus start …`) and let the guest finish bootstrapping (Docker/tooling inside the VM). The first `doctor` run still waits on runtime readiness and can take **several minutes** on Firecracker—this is guest startup and bootstrap, not your `.nexus/probe` scripts alone. On hosts where Nexus selects **seatbelt** instead, `doctor` is usually much faster. There is no separate “fast doctor” flag in this demo; plan time or use `npm run check` for scaffold-only validation.

### Host auth bundle (CLI vs SDK)

- **`nexus create`** builds a tarball on **the machine running the CLI** and sends it as `hostAuthBundle`. There is no separate user-facing bundle subcommand; SDK/CI details are in [`docs/reference/host-auth-bundle.md`](../../docs/reference/host-auth-bundle.md).
- **SDK `workspace.create`:** omitting `hostAuthBundle` sends nothing; the daemon does not invent a bundle. Custom bundles are not re-filtered server-side—match the CLI registry if you need the same contents.

## Worktrees (parallel local branches)

Isolate workstreams without switching branches in a single working tree:

```bash
git worktree add -b feat/ledgerlens-rules-api ../ledgerlens-demo-rules-api
git worktree add -b feat/ledgerlens-ui ../ledgerlens-demo-ui
```

In each worktree, run `nexus init --force` from that directory (or `nexus init --project-root "$(pwd -P)" --force`) if `.nexus` should be tracked per branch.

## Tunnel, exec, doctor

After `nexus start <workspace-id>`:

- **`nexus tunnel <workspace-id>`** — forwards compose ports discovered from `docker-compose.yml` / `docker-compose.yaml` in the project root; blocks until Ctrl-C.
- **`nexus exec --project-root <abs-path> -- <command>`** — absolute `--project-root` required (see above).
- **`nexus doctor`** — same; prefer **`npm run doctor`** from this folder to avoid path mistakes.

Example (manual absolute path):

```bash
nexus doctor --project-root "$(pwd -P)" --suite local
```

(Add a `docker-compose.yml` at the project root when you want tunnel port discovery; this demo ships without one.)

For backend selection, doctor latency, and fork vs workspace vs worktree, see [`docs/tutorials/operations.md`](../../docs/tutorials/operations.md) and [`docs/reference/cli.md`](../../docs/reference/cli.md).

## Layout

- `.nexus/workspace.json` — version `1` only (see `docs/reference/workspace-config.md`).
- `.nexus/lifecycles/*.sh` — setup / start / teardown hooks.
- `.nexus/probe/`, `.nexus/check/` — doctor discovery.
- `scripts/doctor.sh` — wraps `nexus doctor` with a resolved absolute `--project-root`.

## SDK note

When creating workspaces programmatically, pass **`hostAuthBundle`** explicitly if the guest should receive client-side AI tool configs; otherwise the create request sends no bundle.
