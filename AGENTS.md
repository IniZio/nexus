# Agent Guidelines

## Project Overview

Nexus remote workspace core: **Workspace Daemon** (Go, `packages/nexus`) and **Workspace SDK** (TypeScript, `packages/sdk/js`). Keep changes centered on those packages; do not reintroduce removed non-core surfaces.

## Remote-First Architecture

**The daemon may run on a different machine than the user.** Design and verify under that assumption.

- Daemon host paths are not user paths; do not read user credentials from the daemon's `$HOME` and assume they belong to the user.
- Symlink-based credential tricks break when the daemon is remote; user-owned secrets must travel via RPC (`workspace.create` `configBundle`, auth relay at exec time, or explicit client-supplied payloads).

**Host CLI sync:** `nexus create` calls `authbundle.BuildFromHome()` on the **client machine** and sends the result as `configBundle` in `workspace.create`. The daemon never reads the daemon host's `$HOME` for user credentials. Seatbelt delivers the bundle via a host-side temp file (no SSH arg-length limit), then unpacks it in the guest; it does **not** create live symlinks back to the daemon's filesystem.

Flag any feature that reads user-owned data from the daemon filesystem without an explicit client-supplied or relayed payload.

## Enforcement

Complete work fully; verify builds, tests, types, and lint; provide evidence; use isolated worktrees for features (not the main worktree). If stopping early, list what is undone, why, and what the user should do next.

## Documentation

User-facing docs live under `docs/`: `tutorials/`, `reference/`, `dev/` (contributing, roadmap). Only document implemented behavior. Do not document removed module surfaces as current capabilities.

```text
docs/
├── index.md
├── tutorials/
├── reference/   (cli, sdk, workspace-config, host-auth-bundle)
└── dev/         (contributing, roadmap)
```
