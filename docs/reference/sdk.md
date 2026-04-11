# SDK Reference

Nexus SDK is the programmatic surface for remote workspaces. TypeScript client: [`@nexus/sdk`](https://www.npmjs.com/package/@nexus/sdk) (`packages/sdk/js`).

## Install

```bash
npm install @nexus/sdk
```

## Mental Model

- `WorkspaceClient`: one authenticated WebSocket connection. Fields: `endpoint`, `token`, optional `workspaceId`.
- `client.workspaces`: `create`, `open`, `list`, `start`, `stop`, `remove`, `fork`, `pause`, `resume`, `restore`, `mintAuthRelay`, `revokeAuthRelay`, `capabilities`.
- `WorkspaceHandle` (from `create` / `open` / `restore` / `fork`): `id`, `state`, `rootPath`; **`exec`**, **`fs`**, **`spotlight`**, `git`, `service`.
- `client.ssh`: PTY sessions — `open`, `write`, `resize`, `close`, `onData`, `onExit`.
- There is no separate `tunnel` API; use **`ws.spotlight`** (`expose`, `list`, `close`, `applyDefaults`, `applyComposePorts`).

## Quick Start

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

`WorkspaceCreateSpec` fields: `repo`, `workspaceName`, optional `agentProfile`, `ref`, `policy` (`authProfiles`, `sshAgentForward`, `gitCredentialMode`), `backend`, `authBinding`, and `configBundle` (base64 gzip+tar, ≤4 MiB — see [host-auth-bundle.md](host-auth-bundle.md)). Full types: `packages/sdk/js/src/types.ts`.

## Remote client vs remote daemon

Pass auth material via create-time `configBundle` and short-lived relay tokens (`mintAuthRelay` / `revokeAuthRelay`). Do not assume files on the daemon host. See [host-auth-bundle.md](host-auth-bundle.md) for packing rules.

## Related

- CLI: [`cli.md`](cli.md)
- Host auth bundle: [`host-auth-bundle.md`](host-auth-bundle.md)
- Workspace config: [`workspace-config.md`](workspace-config.md)
