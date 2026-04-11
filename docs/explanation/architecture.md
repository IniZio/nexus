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
- Project conventions (repository-root `.nexus/` + compose files)
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

## Layer Boundaries

Target modular layout (incremental migration):

**`packages/nexus`**

```
server/transport/   - websocket framing, sessions
server/rpc/         - method registry, dispatch
server/pty/         - PTY open/write/resize/close
workspace/          - lifecycle, readiness, relations, create flows
runtime/selection/  - single entrypoint for backend selection policy
runtime/drivers/    - backend-specific drivers + shared helpers
storage/            - persistence (SQLite) and record access
git/                - worktree, fork, sync operations
auth/               - relay, bundle, profile mapping
```

**`packages/sdk/js`**

```
rpc/                - connection core, request map, notifications (e.g. rpc/connection.ts)
transport/          - node-websocket and browser-websocket adapters
workspace/          - manager, handle, lifecycle
operations/         - exec, fs, pty, spotlight
auth/               - bundle
types/              - domain-split type files (e.g. types/workspace.ts)
```

**`packages/e2e/flows/`**

```
harness/            - daemon/, repo/, session/, assertions/
cases/              - test suites organized by flow type
parity/             - matrix and contracts
```

## Related Docs

- CLI: `docs/reference/cli.md`
- SDK: `docs/reference/sdk.md`
- Project structure: `docs/reference/project-structure.md`
- Workspace config: `docs/reference/workspace-config.md`
