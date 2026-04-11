# Agent Guidelines

## Project Overview

Nexus remote workspace core: **Workspace Daemon** (Go, `packages/nexus`) and **Workspace SDK** (TypeScript, `packages/sdk/js`). Keep changes centered on those packages; do not reintroduce removed non-core surfaces.

## Remote-First Architecture

**The daemon may run on a different machine than the user.** Design and verify under that assumption.

- Daemon host paths are not user paths; do not read user credentials from the daemon’s `$HOME` and assume they belong to the user.
- Symlink-based credential tricks break when the daemon is remote; user-owned secrets should travel via RPC (`workspace.create` / `AuthBinding`, auth relay at exec time, or explicit client-supplied payloads).

**Host CLI sync:** Firecracker guest bootstrap reads `hostAuthBundle` (base64 gzip+tar) from `workspace.create` when provided; otherwise it only reads the daemon’s home when `useDaemonHostAuthBundle` is true (legacy local single-user). The `nexus workspace create` CLI builds the bundle from the client’s home before RPC.

Flag any other feature that reads user-owned data from the daemon filesystem without an explicit client-supplied or relayed payload.

## Enforcement

Complete work fully; verify builds, tests, types, and lint; provide evidence; use isolated worktrees for features (not the main worktree). If stopping early, list what is undone, why, and what the user should do next.

## Documentation

User-facing docs live under `docs/`: `tutorials/`, `reference/`, `dev/` (contributing, roadmap). Only document implemented behavior. Do not document removed module surfaces as current capabilities.

```text
docs/
├── index.md
├── tutorials/
├── reference/   (cli, sdk, workspace-config)
└── dev/         (contributing, roadmap)
```
