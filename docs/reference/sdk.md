# SDK Reference

Programmatic control of remote workspaces. TypeScript client: `@nexus/sdk` (`packages/sdk/js`).

## Install

```bash
npm install @nexus/sdk
```

## Quick start

```typescript
import { WorkspaceClient } from '@nexus/sdk';

const client = new WorkspaceClient({
  endpoint: process.env.NEXUS_ENDPOINT!,
  token: process.env.NEXUS_TOKEN!,
});

await client.connect();

const ws = await client.workspaces.create({
  repo: '/path/to/repo',
  workspaceName: 'my-workspace',
  agentProfile: 'default',
});

await ws.ready('default');

const out = await ws.exec('node', ['-v']);
console.log(out.stdout.trim());

await ws.writeFile('/workspace/hello.txt', 'hello');
const content = await ws.readFile('/workspace/hello.txt');

await client.workspaces.stop(ws.id);
await client.disconnect();
```

## `WorkspaceClient`

```typescript
const client = new WorkspaceClient({
  endpoint: string,      // daemon WebSocket URL
  token: string,         // auth token
  workspaceId?: string,  // scope to a specific workspace (optional)
  reconnect?: boolean,   // auto-reconnect on drop (default: true)
});

await client.connect();
await client.disconnect();
client.isConnected        // boolean
client.connectionState    // 'disconnected' | 'connecting' | 'connected' | 'reconnecting'
client.onDisconnect(cb)   // register disconnect callback
```

In Node.js environments `WorkspaceClient` also auto-detects and forwards host credentials (git config, SSH keys) when creating workspaces.

## `client.workspaces`

Full lifecycle management. All methods return primitives or a `WorkspaceHandle`.


| Method                  | Returns             | Notes                                                                          |
| ----------------------- | ------------------- | ------------------------------------------------------------------------------ |
| `create(spec)`          | `WorkspaceHandle`   | Provisions a new workspace; auto-bundles host credentials in Node              |
| `start(id)`             | `WorkspaceHandle`   | Starts a stopped workspace and returns a handle; idempotent if already running |
| `list()`                | `WorkspaceRecord[]` | All workspaces                                                                 |
| `stop(id)`              | `boolean`           |                                                                                |
| `remove(id)`            | `boolean`           | Permanently deletes                                                            |
| `restore(id)`           | `WorkspaceHandle`   | Restore from snapshot                                                          |
| `fork(id, name?, ref?)` | `WorkspaceHandle`   | Fork to a new branch                                                           |


### `WorkspaceCreateSpec`

```typescript
{
  repo: string;
  workspaceName: string;
  agentProfile: string;
  ref?: string;
  backend?: string;
  policy?: {
    authProfiles?: ('gitconfig')[];
    sshAgentForward?: boolean;
    gitCredentialMode?: 'host-helper' | 'ephemeral-helper' | 'none';
  };
  authBinding?: Record<string, string>;
}
```

## `WorkspaceHandle`

Returned by `create`, `open`, `restore`, `fork`.

```typescript
ws.id          // string
ws.state       // WorkspaceState
ws.rootPath    // string — absolute path inside the workspace

await ws.ready(checksOrProfile, options?)
```

`ready` accepts either a profile name (`'default'`) or an explicit check array. Options: `timeoutMs`, `intervalMs`.

### Exec

```typescript
await ws.exec('npm', ['test'])
await ws.exec('node', ['-e', 'console.log(1)'], { cwd: '/workspace', timeout: 5000 })
```

Returns `{ stdout, stderr, exitCode }`.

### Filesystem

```typescript
await ws.readFile(path, encoding?)   // string
await ws.writeFile(path, content)
await ws.exists(path)                // boolean
await ws.readdir(path)               // string[]
await ws.mkdir(path, recursive?)
await ws.rm(path, recursive?)
await ws.stat(path)
```

### Tunnels

```typescript
const fwd = await ws.tunnel.add({ remotePort: 5173, localPort: 5173 })
await ws.tunnel.list()     // { forwards: TunnelHandle[] }
await ws.tunnel.stop(id)   // boolean
await fwd.stop()           // boolean — shorthand
```

Port detection (workspace defaults, compose ports) is handled automatically by the daemon. `add` / `stop` are for explicit manual overrides.

## `client.shell`

Low-level PTY access for interactive or streaming use cases.

```typescript
const sessionId = await client.shell.open({
  workspaceId: 'ws-123',
  cols: 120,
  rows: 40,
  workdir: '/workspace',
})

await client.shell.write(sessionId, 'ls\n')
await client.shell.resize(sessionId, 200, 50)
await client.shell.close(sessionId)

const unsubData = client.shell.onData(({ sessionId, data }) => process.stdout.write(data))
const unsubExit = client.shell.onExit(({ sessionId, exitCode }) => { /* ... */ })
unsubData()
unsubExit()
```

## Related

- CLI: `[cli.md](cli.md)`
- Workspace config: `[workspace-config.md](workspace-config.md)`

