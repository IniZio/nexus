# Agent Guidelines

## Project Overview

Nexus remote workspace core: **Workspace Daemon** (Go, `packages/nexus`) and **Workspace SDK** (TypeScript, `packages/sdk/js`). Keep changes centered on those packages; do not reintroduce removed non-core surfaces.

## Remote-First Architecture

**The daemon may run on a different machine than the user.** Design and verify under that assumption.

- Daemon host paths are not user paths; do not read user credentials from the daemon's `$HOME` and assume they belong to the user.
- Symlink-based credential tricks break when the daemon is remote; user-owned secrets must travel via RPC (`workspace.create` `configBundle`, auth relay at exec time, or explicit client-supplied payloads).

**Host CLI sync:** `nexus create` calls `authbundle.BuildFromHome()` on the **client machine** and sends the result as `configBundle` in `workspace.create`. The daemon never reads the daemon host's `$HOME` for user credentials. Seatbelt delivers the bundle via a host-side temp file (no SSH arg-length limit), then unpacks it in the guest; it does **not** create live symlinks back to the daemon's filesystem.

Flag any feature that reads user-owned data from the daemon filesystem without an explicit client-supplied or relayed payload.

## Code Structure Policy

**Tiered file-size limits:**

```
Core/domain logic:              <= 300 lines
Orchestration/application logic: <= 400 lines
Transport/adapters/tests:        <= 500 lines
Generated files:                 exempt
```

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

```
packages/nexus/pkg/server/server.go          (~1209 lines)
packages/nexus/pkg/handlers/workspace_manager.go (~660 lines)
packages/sdk/js/src/types.ts                 (~376 lines)
```

## Enforcement

Complete work fully; verify builds, tests, types, and lint; provide evidence; use isolated worktrees for features (not the main worktree). If stopping early, list what is undone, why, and what the user should do next.

## Documentation

User-facing docs live under `docs/`: `explanation/`, `reference/`, `superpowers/`, `tutorials/`, and `dev/` (contributing, roadmap). Only document implemented behavior. Do not document removed module surfaces as current capabilities.

```text
docs/
├── index.md
├── explanation/
├── reference/   (cli, sdk, workspace-config, host-auth-bundle)
├── superpowers/
├── tutorials/
└── dev/         (contributing, roadmap)
```

## Project scaffold

Nexus lifecycle and doctor conventions use **`.nexus/` at the repository root** only (`nexus init`, `nexus doctor`). There is no second copy under `packages/nexus/`.
