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

## Create spec

`WorkspaceCreateSpec` includes `repo`, `workspaceName`, `agentProfile`, optional `ref`, `policy` (`authProfiles`, `sshAgentForward`, `gitCredentialMode`), and optional `backend`. Types: `packages/sdk/js/src/types.ts`.

## Remote client vs remote daemon

Prefer passing auth material via create-time policy / stored `AuthBinding` and short-lived relay tokens (`mintAuthRelay` / `revokeAuthRelay`) rather than assuming files on the daemon host. Daemon-side behavior that still reads the daemon’s home for tooling configs is a known limitation for fully remote daemons (see Firecracker driver host auth bundle).

## Related

- CLI: [`cli.md`](cli.md)
- Workspace config: [`workspace-config.md`](workspace-config.md)
