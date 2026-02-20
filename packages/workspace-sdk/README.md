# @nexus/workspace-sdk

TypeScript SDK for connecting to remote Nexus workspaces via WebSocket.

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

## License

MIT
