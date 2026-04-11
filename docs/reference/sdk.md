# SDK Reference

Nexus SDK is the programmatic surface for remote workspaces.
This page is the canonical SDK interface.

## Install (one line)

```bash
npm install @nexus/sdk
```

## Mental Model

- `WorkspaceClient`: one authenticated WebSocket connection.
- `client.workspaces`: create/open/list/start/stop/remove/fork workspaces.
- `WorkspaceHandle`: do work inside one workspace (`exec`, files, `tunnel`, `git`, `service`).
- `client.ssh`: interactive terminal sessions.
- `TunnelHandle`: returned by `ws.tunnel.start(...)`, includes `stop()` for explicit close.

If you automate more than one workspace, use `WorkspaceHandle` for explicit scope.

## Quick Start

```typescript
import { WorkspaceClient } from '@nexus/sdk';

const client = new WorkspaceClient({
  endpoint: process.env.NEXUS_ENDPOINT!,
  token: process.env.NEXUS_TOKEN!,
});

await client.connect();

const ws = await client.workspaces.open('ws-123');
const result = await ws.exec('node', ['-v']);
console.log(result.stdout.trim());

const pkg = await ws.readFile('/workspace/package.json', 'utf8');
console.log(pkg.slice(0, 80));

const tunnel = await ws.tunnel.start({
  service: 'web',
  remotePort: 5173,
  localPort: 5173,
});
await tunnel.stop();

await ws.stop();
await ws.remove();

await client.disconnect();
```

## Minimal API Surface

`WorkspaceClient` config:

- `endpoint` (required)
- `token` (required)
- `workspaceId` (optional default workspace id in daemon connection)

Most-used methods:

- `client.connect()`
- `client.disconnect()`
- `client.workspaces.create(spec)`
- `client.workspaces.open(id)`
- `client.workspaces.list()`
- `client.workspaces.start(id)`
- `client.workspaces.stop(id)`
- `client.workspaces.remove(id)`

Workspace operations (`WorkspaceHandle`):

- `ws.exec(command, args?, options?)`
- `ws.readFile(path, encoding?)`
- `ws.writeFile(path, content)`
- `ws.readdir(path)`
- `ws.exists(path)`
- `ws.start()`, `ws.stop()`, `ws.remove()`
- `ws.ready(checks, options?)` or `ws.readyProfile(profile, options?)`
- `ws.tunnel.start({ service, remotePort, localPort })`

## Advanced Operations

- Git passthrough: `ws.git(action, params?)`
- Service manager: `ws.service(action, params?)`
- Lifecycle extras: `client.workspaces.pause(id)`, `client.workspaces.resume(id)`, `client.workspaces.restore(id)`, `client.workspaces.fork(id, name?, ref?)`
- Capability checks: `client.workspaces.capabilities()`
- Auth relay: `client.workspaces.mintAuthRelay(...)`, `client.workspaces.revokeAuthRelay(token)`
- SSH sessions: `client.ssh.open(...)`, `client.ssh.write(...)`, `client.ssh.resize(...)`, `client.ssh.close(...)`

## Client and Daemon on Different Machines

Nexus supports a split deployment where the SDK client and daemon are on different hosts.

- Credentials should originate from the client request (`workspace.create.spec.authBinding`) and be injected through one-time relay tokens (`mintAuthRelay`).
- Do not depend on daemon host login state or daemon-local OAuth files for normal command execution.
- Command execution uses a minimal baseline environment (`PATH`, `HOME`, shell/runtime essentials), then adds explicit relay variables. This avoids leaking daemon host secrets into workspace processes.
- Host credential sync is always enabled for supported runtime bootstrapping paths.

For remote usage, the recommended flow is:

1. Client opens daemon connection.
2. Client creates workspace with per-binding auth values.
3. Client mints short-lived relay token for a specific binding.
4. Client runs `exec`/`ssh` with `authRelayToken`.

## Conventions and Defaults

- Compose ports can be forwarded by convention through `ws.tunnel.applyComposePorts()`.
- Default forwards can be applied with `ws.tunnel.applyDefaults()`.
- Project config shape lives in `docs/reference/workspace-config.md`.

## Related Docs

- CLI: `docs/reference/cli.md`
- Project structure: `docs/reference/project-structure.md`
- Workspace config: `docs/reference/workspace-config.md`
- Architecture: `docs/explanation/architecture.md`

