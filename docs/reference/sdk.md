# SDK Reference

TypeScript client for the Nexus workspace daemon: [`@nexus/sdk`](https://www.npmjs.com/package/@nexus/sdk) (`packages/sdk/js`).

## Install

```bash
npm install @nexus/sdk
```

## Overview

- **`WorkspaceClient`**: WebSocket JSON-RPC to the daemon. Fields: `endpoint`, `token`, optional `workspaceId` (default workspace for methods that need it), reconnect options. Exposes `workspaces` and `ssh`.
- **`client.workspaces`**: `create`, `open`, `list`, `relations`, `remove`, `stop`, `start`, `restore`, `pause`, `resume`, `fork`, `mintAuthRelay`, `revokeAuthRelay`, `capabilities`.
- **`WorkspaceHandle`** (from `create` / `open` / `restore` / `fork`): `id`, `state`, `rootPath`; **`exec`**, **`fs`**, **`spotlight`** (port forwards); `info`, `ready`, `readyProfile`, `git`, `service`.
- **`client.ssh`**: PTY sessions — `open`, `write`, `resize`, `close`, `onData`, `onExit` (see `pty.ts`).

There is no separate `tunnel` API in the SDK; host↔workspace port exposure uses **`workspaceHandle.spotlight`** (`expose`, `list`, `close`, `applyDefaults`, `applyComposePorts`).

## Example

```typescript
import { WorkspaceClient } from '@nexus/sdk';

const client = new WorkspaceClient({
  endpoint: process.env.NEXUS_ENDPOINT!,
  token: process.env.NEXUS_TOKEN!,
});

await client.connect();

const ws = await client.workspaces.open('ws-123');

const out = await ws.exec.exec('node', ['-v']);
console.log(out.stdout.trim());

const pkg = await ws.fs.readFile('/workspace/package.json', 'utf8');

await ws.spotlight.expose({ service: 'web', remotePort: 5173, localPort: 5173 });

await client.workspaces.stop(ws.id);
await client.workspaces.remove(ws.id);

await client.disconnect();
```

## `workspace.create` spec

`WorkspaceCreateSpec` (`packages/sdk/js/src/types.ts`) includes:

| Field | Role |
|--------|------|
| `repo`, `workspaceName`, `agentProfile` | Required for create |
| `ref`, `policy`, `backend` | Optional |
| `hostAuthBundle` | Optional. Base64-encoded **gzip-compressed tar** (paths relative to `$HOME` as in the CLI). **Max 4MiB decoded**; invalid base64 or over limit is rejected. The daemon does **not** re-filter contents—for parity with `nexus create`, follow [`host-auth-bundle.md`](host-auth-bundle.md) or call `authbundle.BuildFromHome()` from Go. If omitted, **no** tarball is sent. |

For command execution and API keys, prefer **`AuthBinding`** on the workspace and **`mintAuthRelay` / `revokeAuthRelay`** instead of assuming files on the daemon host.

## Remote daemon

- **Secrets and API keys:** Use `policy` / `AuthBinding` and auth relay tokens for `exec`/`ssh`, not daemon-local OAuth files.
- **Tooling config tarball:** Supply `hostAuthBundle` from the **client** when you need guest copies of opencode/codex/claude-style configs. The CLI builds it via `BuildFromHome()` on the machine running `nexus create`, using the selective file registry above.

## Related

- CLI: [`cli.md`](cli.md)
- Host auth bundle: [`host-auth-bundle.md`](host-auth-bundle.md)
- Workspace config: [`workspace-config.md`](workspace-config.md)
