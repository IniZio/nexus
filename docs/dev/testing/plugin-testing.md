# Nexus Plugin Testing Infrastructure

## Overview

This document describes how to test the Nexus plugin hooks using `opencode run`.

## Available Testing Methods

### Method 1: `opencode run` (Recommended)

Use the CLI to send messages and trigger tool execution:

```bash
# Trigger tool execution
opencode run "check boulder status"

# Send chat messages
opencode run "hello world"
```

### Method 2: Direct State File Inspection

The plugin stores state in `.nexus/boulder/state.json`:

```json
{
  "iteration": 0,
  "lastActivity": 1771666560622,
  "lastEnforcement": 0,
  "status": "CONTINUOUS"
}
```

## Hook Testing Endpoints

### 1. Tool Execution (`tool.execute.before`)

**Trigger**: Run any tool via `opencode run`

**Verification**: Check `lastActivity` timestamp in state file

```bash
# Before
cat .nexus/boulder/state.json | jq .lastActivity

# Run tool
opencode run "list files"

# After - should be updated
cat .nexus/boulder/state.json | jq .lastActivity
```

### 2. Chat Input (`chat.input`)

**Trigger**: Send message while status is `ENFORCING`

**Verification**: Status changes from `ENFORCING` to `CONTINUOUS`

```bash
# Set to ENFORCING
echo '{"iteration":1,"lastActivity":0,"lastEnforcement":0,"status":"ENFORCING"}' > .nexus/boulder/state.json

# Send chat message
opencode run "hello world"

# Check status - should be CONTINUOUS now
cat .nexus/boulder/state.json | jq .status
```

### 3. Idle Events (`session.idle`)

**Trigger**: Emit `session.idle` event with `idleTime >= 30000`

**Note**: This requires programmatic event injection. The event hook:
- Only triggers when `idleTime >= 30000` (30 seconds)
- Only triggers when status is `PAUSED`
- Sets status to `ENFORCING` and increments iteration

**Manual test**:
```bash
# Set to PAUSED
echo '{"iteration":1,"lastActivity":0,"lastEnforcement":0,"status":"PAUSED"}' > .nexus/boulder/state.json

# Currently no CLI way to emit session.idle event
# Would need to modify plugin or use MCP/ACP client
```

## Automated Test Script

See `test-plugin.sh` for a bash-based test suite.

## Limitations

1. **No direct API**: `opencode serve` serves web UI, not a REST API
2. **No event CLI**: No command to emit `session.idle` events
3. **ACP requires client**: `opencode acp` exits without persistent client

## Alternative: Direct Module Testing

For unit testing, you can import the plugin directly:

```typescript
import { createOpenCodePlugin } from './packages/opencode/src/index';

const plugin = await createOpenCodePlugin({
  directory: '/test/path',
  client: { app: { log: console.log }, tui: { showToast: () => {} } }
});

// Test tool.execute.before
await plugin['tool.execute.before']({ tool: 'Read' }, {});

// Test event
await plugin.event({ event: 'session.idle', data: { idleTime: 60000 } }, {});

// Test chat.input
await plugin['chat.input']({ message: 'test' }, {});
```
