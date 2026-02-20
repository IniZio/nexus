# @nexus/opencode-plugin

OpenCode plugin for Nexus Workspace SDK integration.

## Features

- Remote file operations (read/write/edit)
- Remote command execution
- Activity tracking and idle detection
- Automatic keep-alive pinging
- Configuration via `opencode.json`

## Installation

Add to your OpenCode configuration:

```json
{
  "plugin": ["@nexus/opencode-plugin"]
}
```

## Configuration

Configure the plugin in your `opencode.json`:

```json
{
  "nexus": {
    "workspace": {
      "endpoint": "wss://workspace.nexus.dev",
      "workspaceId": "my-project",
      "token": "${NEXUS_TOKEN}"
    },
    "options": {
      "enableFileOperations": true,
      "enableShellExecution": true,
      "idleTimeout": 300000,
      "keepAliveInterval": 60000,
      "excludedPaths": ["/home/newman/magic/nexus/.claude", "/home/newman/magic/nexus/.omc"],
      "largeFileThreshold": 10485760
    }
  }
}
```

### Configuration Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `endpoint` | string | - | WebSocket endpoint for the workspace |
| `workspaceId` | string | - | Unique workspace identifier |
| `token` | string | - | Authentication token (supports `${VAR}` syntax) |
| `enableFileOperations` | boolean | `true` | Enable remote file operations |
| `enableShellExecution` | boolean | `true` | Enable remote shell execution |
| `idleTimeout` | number | `300000` | Idle timeout in milliseconds |
| `keepAliveInterval` | number | `60000` | Keep-alive ping interval in ms |
| `excludedPaths` | string[] | `[]` | Paths to always use local filesystem |
| `largeFileThreshold` | number | `10485760` | Files larger than this use local fs |

## Commands

- `/nexus-connect` - Connect to the Nexus Workspace
- `/nexus-status` - Show current connection status
- `/nexus-disconnect` - Disconnect from the workspace

## Supported Tools

The plugin intercepts these OpenCode tools:

- `read` - Routes to `workspace.fs.readFile()`
- `write` - Routes to `workspace.fs.writeFile()`
- `edit` - Routes to `workspace.fs.readFile()` + `writeFile()`
- `bash` - Routes to `workspace.exec()` (when enabled)

## Environment Variables

Set your Nexus token in an environment variable:

```bash
export NEXUS_TOKEN="your-token-here"
```

## Building

```bash
npm install
npm run build
```
