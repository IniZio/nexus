# Workspace Core Architecture

Nexus keeps architecture intentionally small: daemon + SDK + project conventions.

## Three Layers

- `packages/nexus` (Go daemon)
  - JSON-RPC over WebSocket
  - Workspace lifecycle and handlers
  - Service and readiness control
  - Spotlight forwards and compose discovery
- `packages/sdk/js` (`@nexus/sdk`)
  - Authenticated client transport
  - Workspace lifecycle APIs
  - Scoped workspace handles for `fs`, `exec`, `spotlight`, `git`, and `service`
- Project conventions (`.nexus/` + compose files)
  - Lifecycle scripts and doctor probes/checks
  - Minimal config and file-driven defaults

## Request Flow

1. SDK connects to daemon over authenticated WebSocket.
2. Client creates or opens a workspace.
3. Operations run through workspace-scoped handlers.
4. Results return as JSON-RPC responses.

## Why It Feels Minimal

- Most projects run with `nexus init` and default conventions.
- Runtime/backend selection is automatic.
- Port forwarding can be convention-driven from compose files.

## Related Docs

- CLI: `docs/reference/cli.md`
- SDK: `docs/reference/sdk.md`
- Project structure: `docs/reference/project-structure.md`
- Workspace config: `docs/reference/workspace-config.md`
