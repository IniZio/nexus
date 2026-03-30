# Workspace SDK

The Nexus Workspace SDK provides a TypeScript/JavaScript interface for connecting to remote Nexus workspaces via WebSocket. It enables programmatic control over workspace file systems, process execution, and service management.

## Installation

```bash
npm install @nexus/workspace-sdk
```

## Quick Start

```typescript
import { WorkspaceClient } from '@nexus/workspace-sdk';

const client = new WorkspaceClient({
  endpoint: 'wss://workspace.nexus.dev',
  workspaceId: 'my-project',
  token: process.env.NEXUS_TOKEN,
});

await client.connect();

// Read a file
const content = await client.fs.readFile('/workspace/src/index.ts', 'utf8');

// Write a file
await client.fs.writeFile('/workspace/test.txt', 'Hello, World!');

// List directory
const files = await client.fs.readdir('/workspace/src');

// Execute a command
const result = await client.exec('npm', ['run', 'build']);
console.log(result.stdout);

await client.disconnect();
```

## API Reference

### WorkspaceClient

The main class for connecting to a remote workspace.

```typescript
const client = new WorkspaceClient(config);
```

#### Configuration

| Property | Type | Required | Description |
|----------|------|----------|-------------|
| `endpoint` | string | Yes | WebSocket endpoint URL |
| `workspaceId` | string | Yes | Workspace identifier |
| `token` | string | Yes | Authentication token |
| `reconnect` | boolean | No | Enable auto-reconnect (default: true) |
| `reconnectDelay` | number | No | Initial reconnect delay in ms (default: 1000) |
| `maxReconnectAttempts` | number | No | Max reconnect attempts (default: 10) |

#### Methods

- `connect()` - Establish WebSocket connection
- `disconnect()` - Close WebSocket connection
- `isConnected()` - Check connection status
- `onDisconnect(callback)` - Register disconnect handler

### Workspace Manager (`client.workspace`)

- `create(spec)` - Create remote isolated workspace
- `open(id)` - Open existing workspace handle
- `list()` - List workspaces
- `remove(id)` - Remove workspace

```typescript
const ws = await client.workspace.create({
  repo: '<internal-repo-url>',
  ref: 'main',
  workspaceName: 'workspace-web-agent-1',
  agentProfile: 'default',
  policy: {
    authProfiles: ['gitconfig'],
    sshAgentForward: true,
    gitCredentialMode: 'host-helper',
  },
});
```

### Spotlight (`WorkspaceHandle.spotlight`)

- `expose({ service, remotePort, localPort, host? })`
- `list()`
- `close(id)`
- `applyDefaults()`
- `applyComposePorts()`

```typescript
await ws.spotlight.expose({ service: 'student-portal', remotePort: 5173, localPort: 5173 });
await ws.spotlight.expose({ service: 'api', remotePort: 8000, localPort: 8000 });
```

### Git and service commands

```typescript
await ws.git('status');
await ws.git('revParse', { ref: 'HEAD' });

await ws.service('start', {
  name: 'api',
  command: 'pnpm',
  args: ['--dir', 'web', 'dev'],
  autoRestart: true,
  maxRestarts: 2,
  restartDelayMs: 250,
  stopTimeoutMs: 1500,
});

await ws.service('status', { name: 'api' });
await ws.service('logs', { name: 'api' });
await ws.service('stop', { name: 'api', stopTimeoutMs: 1500 });

const ready = await ws.ready(
  [{ name: 'api-health', command: 'sh', args: ['-lc', 'curl -fsS http://localhost:8000/health || exit 1'] }],
  { timeoutMs: 15000, intervalMs: 500 }
);

if (!ready.ready) throw new Error('workspace not ready');

const profiled = await ws.readyProfile('default-services', { timeoutMs: 10000, intervalMs: 500 });
if (!profiled.ready) throw new Error('workspace profile not ready');

const defaults = await ws.spotlight.applyDefaults();
console.log(defaults.forwards.length);

const composeForwards = await ws.spotlight.applyComposePorts();
console.log(composeForwards.forwards.length, composeForwards.errors.length);
```

Convention-over-configuration behavior:

- Compose projects are auto-detected by daemon (`docker-compose.yml` / `docker-compose.yaml`).
- All published compose ports are auto-forwarded on `workspace.ready`.
- `applyComposePorts()` is available for explicit/manual triggering.
- ACP integration is capability-aware: if `opencode` is not installed, ACP checks are skipped rather than failing readiness.

Project config defaults and schema are documented in `docs/reference/workspace-config.md`.

### File System API (`client.fs`)

- `readFile(path, encoding?)` - Read file contents
- `writeFile(path, content)` - Write file contents
- `exists(path)` - Check if file/directory exists
- `readdir(path)` - List directory contents
- `mkdir(path, recursive?)` - Create directory
- `rm(path, recursive?)` - Remove file or directory
- `stat(path)` - Get file/directory metadata

### Command Execution (`client.exec`)

- `exec(command, args?, options?)` - Execute command and capture output

```typescript
interface ExecOptions {
  cwd?: string;
  env?: Record<string, string>;
  timeout?: number;
}
```

## Protocol

This SDK uses JSON-RPC 2.0 over WebSocket for communication with the workspace daemon.

Example request:
```json
{
  "jsonrpc": "2.0",
  "id": "req-123",
  "method": "fs.readFile",
  "params": { "path": "/workspace/src/index.ts", "encoding": "utf8" }
}
```

Example response:
```json
{
  "jsonrpc": "2.0",
  "id": "req-123",
  "result": { "content": "console.log('hello');", "encoding": "utf8" }
}
```
