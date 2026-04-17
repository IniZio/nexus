# Agent Guidelines

## Project Overview

Nexus remote workspace core: **Workspace Daemon** (Go, `packages/nexus`). Keep changes centered on that package; do not reintroduce removed non-core surfaces.

**Architecture is firecracker-only.** The only supported VM backend is Firecracker. Lima was removed. The `process` sandbox driver provides process-isolation fallback for environments where VMs are unavailable.

## Remote-First Architecture

**The daemon may run on a different machine than the user.** Design and verify under that assumption.

- Daemon host paths are not user paths; do not read user credentials from the daemon's `$HOME` and assume they belong to the user.
- Symlink-based credential tricks break when the daemon is remote; user-owned secrets must travel via RPC (`workspace.create` `configBundle`, auth relay at exec time, or explicit client-supplied payloads).

**Host CLI sync:** `nexus create` calls `authbundle.BuildFromHome()` on the **client machine** and sends the result as `configBundle` in `workspace.create`. The daemon never reads the daemon host's `$HOME` for user credentials. Seatbelt delivers the bundle via a host-side temp file (no SSH arg-length limit), then unpacks it in the guest; it does **not** create live symlinks back to the daemon's filesystem.

Flag any feature that reads user-owned data from the daemon filesystem without an explicit client-supplied or relayed payload.

## Code Structure Policy

**Tiered file-size limits:**

| Layer | Limit |
|---|---|
| Core/domain logic | ≤300 lines |
| Orchestration/application logic | ≤400 lines |
| Transport/adapters/tests | ≤500 lines |
| Generated files | exempt |

**Dependency direction rules:**

```
domain          → no project-specific dependencies
orchestration   → may depend on domain
transport       → may depend on application/domain
storage         → implements domain/application-owned interfaces
tests           → may depend on any layer; keep harness code modular
```

**Concept naming conventions:**

```
transport/    wire protocol, sockets, adapters, sessions
storage/      persistence and backing stores
runtime/      backend selection, preflight, driver-specific behavior
workspace/    lifecycle, readiness, relations, create/fork/restore flows
auth/         relay, bundle, profile mapping
rpc/          method registration, DTOs, middleware
harness/      reusable e2e support code only
```

**Known debt (tracked, not instant failures):**

| File | Lines | Status |
|---|---|---|
| `packages/nexus/pkg/handlers/workspace_manager.go` | ~1246 | over 400 limit |
| `packages/nexus/pkg/workspacemgr/manager.go` | ~1268 | over 300/400 limits |

## Agent Skills

Nexus ships two layers of agent skills:

### Project skills (`.opencode/skills/`)

Loaded automatically for this repository. Each is a directory with a `SKILL.md` following the [Agent Skills spec](https://agentskills.io/specification).

| Skill | Description |
|---|---|
| `nexus-macos-dev` | Build, run, and test NexusApp.app and its embedded daemon |
| `bumping-app-version` | Bump or update the app version number |
| `creating-git-commits` | Create commits matching repository conventions |
| `superpowers` | Workspace superpowers (experimental) |

### Groundwork workflow skills (`~/.config/opencode/plugins/groundwork/skills/`)

Loaded by the opencode agent automatically. Used for structured development workflows.

| Skill | When to invoke |
|---|---|
| `create-prd` | Starting a non-trivial feature (≥1 day estimated); no master PRD exists |
| `advisor-gate` | Any technical decision with uncertainty; always at task completion before declaring done |
| `nested-prd` | Architectural pivot or scope increase >1 day discovered mid-implementation |
| `bdd-implement` | Any visible UI change or bug fix on macOS or web |
| `commit` | Creating git commits |
| `session-continue` | Context window growing long or fresh session needed |
| `consolidate-docs` | Cleaning up PRDs after iterations; preparing for handoff or release |
| `opencode-acp` | Controlling another opencode instance via ACP protocol |

## Enforcement

Complete work fully; verify builds, tests, types, and lint; provide evidence; use isolated worktrees for features (not the main worktree). If stopping early, list what is undone, why, and what the user should do next.

## Documentation

User-facing docs live under `docs/`: `guides/`, `reference/`. Contributing guidance is in `CONTRIBUTING.md` at the repository root. Only document implemented behavior. Do not document removed module surfaces as current capabilities.

```
docs/
├── README.md
├── guides/
│   ├── installation.md
│   ├── operations.md
│   ├── release-signing.md
│   └── testing.md
└── reference/
    ├── cli.md
    ├── workspace-config.md
    ├── host-auth-bundle.md
    └── project-structure.md
```

## Project scaffold

Nexus lifecycle and doctor conventions use **`.nexus/` at the repository root** only (`nexus init`, `nexus doctor`). There is no second copy under `packages/nexus/`.
