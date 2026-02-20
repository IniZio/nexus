# OpenCode MCP (Model Context Protocol) Design

## Overview

An MCP server that provides programmatic control over OpenCode, enabling true end-to-end testing and automation.

## Why MCP?

**Current Testing Limitations:**
- File-based state monitoring (indirect)
- Can't interact with UI elements
- Can't verify toast notifications visually
- Can't test actual user workflows

**MCP Benefits:**
- Direct control of OpenCode instance
- Access to UI state (toasts, messages, panels)
- Programmatic message sending
- Screenshot/visual verification
- Real user workflow automation

## Architecture

```
┌─────────────────┐     MCP Protocol      ┌─────────────────┐
│   Test Runner   │◄─────────────────────►│  OpenCode MCP   │
│   (Claude/AI)   │    (stdio/sse)       │    Server       │
└─────────────────┘                       └────────┬────────┘
                                                   │
                              WebSocket/IPC        │
                                                   ▼
                                          ┌─────────────────┐
                                          │  OpenCode App   │
                                          │  (Electron/TUI) │
                                          └─────────────────┘
```

## MCP Tools

### 1. Session Management

```typescript
// mcp-server/tools/session.ts

interface CreateSessionArgs {
  directory: string;
  agent?: string;
  model?: string;
}

interface SessionInfo {
  id: string;
  directory: string;
  status: 'active' | 'idle' | 'error';
  messageCount: number;
  lastActivity: number;
}

// Tool: create_session
{
  name: 'create_session',
  description: 'Create a new OpenCode session',
  inputSchema: {
    type: 'object',
    properties: {
      directory: { type: 'string', description: 'Working directory' },
      agent: { type: 'string', description: 'Agent type (e.g., claude, gpt)' },
      model: { type: 'string', description: 'Model identifier' }
    },
    required: ['directory']
  }
}

// Tool: list_sessions
{
  name: 'list_sessions',
  description: 'List all active sessions',
  inputSchema: { type: 'object' }
}

// Tool: get_session
{
  name: 'get_session',
  description: 'Get session details',
  inputSchema: {
    type: 'object',
    properties: {
      sessionId: { type: 'string' }
    },
    required: ['sessionId']
  }
}

// Tool: close_session
{
  name: 'close_session',
  description: 'Close a session',
  inputSchema: {
    type: 'object',
    properties: {
      sessionId: { type: 'string' }
    },
    required: ['sessionId']
  }
}
```

### 2. Message Operations

```typescript
// mcp-server/tools/message.ts

// Tool: send_message
{
  name: 'send_message',
  description: 'Send a message to OpenCode session',
  inputSchema: {
    type: 'object',
    properties: {
      sessionId: { type: 'string' },
      content: { type: 'string' },
      waitForResponse: { type: 'boolean', default: true },
      timeout: { type: 'number', default: 30000 }
    },
    required: ['sessionId', 'content']
  }
}

// Tool: get_messages
{
  name: 'get_messages',
  description: 'Get all messages in session',
  inputSchema: {
    type: 'object',
    properties: {
      sessionId: { type: 'string' },
      limit: { type: 'number', default: 50 },
      since: { type: 'number', description: 'Timestamp to get messages after' }
    },
    required: ['sessionId']
  }
}

// Tool: wait_for_message
{
  name: 'wait_for_message',
  description: 'Wait for specific message pattern',
  inputSchema: {
    type: 'object',
    properties: {
      sessionId: { type: 'string' },
      pattern: { type: 'string', description: 'Regex pattern to match' },
      timeout: { type: 'number', default: 30000 }
    },
    required: ['sessionId', 'pattern']
  }
}
```

### 3. UI Monitoring

```typescript
// mcp-server/tools/ui.ts

// Tool: get_toasts
{
  name: 'get_toasts',
  description: 'Get current toast notifications',
  inputSchema: {
    type: 'object',
    properties: {
      sessionId: { type: 'string' }
    }
  }
}

// Tool: wait_for_toast
{
  name: 'wait_for_toast',
  description: 'Wait for specific toast message',
  inputSchema: {
    type: 'object',
    properties: {
      sessionId: { type: 'string' },
      pattern: { type: 'string', description: 'Regex pattern to match toast' },
      timeout: { type: 'number', default: 10000 }
    },
    required: ['sessionId', 'pattern']
  }
}

// Tool: get_tui_state
{
  name: 'get_tui_state',
  description: 'Get TUI (Terminal UI) current state',
  inputSchema: {
    type: 'object',
    properties: {
      sessionId: { type: 'string' }
    }
  }
}

// Tool: take_screenshot
{
  name: 'take_screenshot',
  description: 'Take screenshot of OpenCode window',
  inputSchema: {
    type: 'object',
    properties: {
      sessionId: { type: 'string' },
      savePath: { type: 'string' }
    }
  }
}
```

### 4. Tool Execution

```typescript
// mcp-server/tools/tools.ts

// Tool: execute_tool
{
  name: 'execute_tool',
  description: 'Execute an OpenCode tool programmatically',
  inputSchema: {
    type: 'object',
    properties: {
      sessionId: { type: 'string' },
      tool: { type: 'string', enum: ['read', 'write', 'bash', 'edit', 'grep'] },
      args: { type: 'object' },
      waitForCompletion: { type: 'boolean', default: true }
    },
    required: ['sessionId', 'tool', 'args']
  }
}

// Tool: get_tool_history
{
  name: 'get_tool_history',
  description: 'Get history of tool executions',
  inputSchema: {
    type: 'object',
    properties: {
      sessionId: { type: 'string' },
      limit: { type: 'number', default: 20 }
    }
  }
}
```

### 5. Plugin/Extension Management

```typescript
// mcp-server/tools/plugins.ts

// Tool: list_plugins
{
  name: 'list_plugins',
  description: 'List loaded plugins',
  inputSchema: { type: 'object' }
}

// Tool: get_plugin_state
{
  name: 'get_plugin_state',
  description: 'Get plugin state (e.g., boulder iteration)',
  inputSchema: {
    type: 'object',
    properties: {
      pluginName: { type: 'string' },
      stateKey: { type: 'string' }
    }
  }
}

// Tool: trigger_plugin_action
{
  name: 'trigger_plugin_action',
  description: 'Trigger plugin-specific action',
  inputSchema: {
    type: 'object',
    properties: {
      pluginName: { type: 'string' },
      action: { type: 'string' },
      args: { type: 'object' }
    }
  }
}
```

## MCP Resources

```typescript
// Resources provide current state

// Resource: session://{sessionId}
{
  uri: 'session://{sessionId}',
  name: 'Session State',
  description: 'Current state of an OpenCode session',
  mimeType: 'application/json'
}

// Resource: messages://{sessionId}
{
  uri: 'messages://{sessionId}',
  name: 'Session Messages',
  description: 'All messages in a session',
  mimeType: 'application/json'
}

// Resource: boulder://state
{
  uri: 'boulder://state',
  name: 'Boulder State',
  description: 'Current boulder enforcement state',
  mimeType: 'application/json'
}
```

## MCP Prompts

```typescript
// Prompts for common testing workflows

// Prompt: test_boulder_idle_detection
{
  name: 'test_boulder_idle_detection',
  description: 'Test boulder idle detection',
  arguments: [
    {
      name: 'sessionId',
      description: 'Session to test',
      required: true
    },
    {
      name: 'idleTime',
      description: 'Seconds to wait for idle',
      default: 35
    }
  ]
}

// Prompt: test_boulder_completion
{
  name: 'test_boulder_completion',
  description: 'Test boulder completion detection',
  arguments: [
    {
      name: 'sessionId',
      description: 'Session to test',
      required: true
    }
  ]
}
```

## Implementation Example: Boulder Test

```typescript
// Example: Testing boulder with MCP

async function testBoulderIdleDetection() {
  // 1. Create session
  const session = await mcpClient.callTool('create_session', {
    directory: '/home/newman/magic/nexus',
    agent: 'claude'
  });
  
  // 2. Send initial message
  await mcpClient.callTool('send_message', {
    sessionId: session.id,
    content: 'Starting boulder test'
  });
  
  // 3. Wait for boulder toast
  const toast = await mcpClient.callTool('wait_for_toast', {
    sessionId: session.id,
    pattern: 'BOULDER ENFORCEMENT',
    timeout: 35000
  });
  
  // 4. Verify system message
  const messages = await mcpClient.callTool('get_messages', {
    sessionId: session.id,
    since: Date.now() - 40000
  });
  
  const hasSystemMessage = messages.some(m => 
    m.content.includes('BOULDER ENFORCEMENT')
  );
  
  // 5. Check boulder state
  const boulderState = await mcpClient.readResource('boulder://state');
  
  // 6. Assert
  assert(toast.found, 'Toast should appear');
  assert(hasSystemMessage, 'System message should appear');
  assert(boulderState.iteration > 0, 'Iteration should increase');
  
  // 7. Cleanup
  await mcpClient.callTool('close_session', { sessionId: session.id });
}
```

## Benefits for Boulder Testing

| Current Testing | MCP-Based Testing |
|----------------|-------------------|
| File polling | Direct UI access |
| Can't verify toasts | `wait_for_toast()` tool |
| Can't verify messages | `get_messages()` tool |
| State file guessing | `get_plugin_state()` tool |
| No visual confirmation | `take_screenshot()` tool |
| Async race conditions | Synchronous tool calls |

## Implementation Plan

### Phase 1: Basic MCP Server
- Session management tools
- Message send/receive
- Basic UI monitoring

### Phase 2: Advanced Features  
- Screenshot capability
- Toast notification waiting
- Plugin state access

### Phase 3: Multi-Session Support
- Parallel session testing
- Session synchronization
- Cross-session assertions

### Phase 4: CI/CD Integration
- GitHub Actions support
- Test report generation
- Screenshot artifacts

## Files to Create

```
.opencode/mcp/
├── server/
│   ├── index.ts              # MCP server entry
│   ├── tools/
│   │   ├── session.ts        # Session tools
│   │   ├── message.ts        # Message tools
│   │   ├── ui.ts             # UI monitoring tools
│   │   └── plugin.ts         # Plugin access tools
│   ├── resources/
│   │   └── index.ts          # Resource handlers
│   └── transport/
│       ├── stdio.ts          # stdio transport
│       └── websocket.ts      # WebSocket transport
├── client/
│   └── index.ts              # MCP client for tests
└── tests/
    ├── boulder.test.ts       # Boulder E2E tests
    └── integration.test.ts   # Integration tests
```

## Next Steps

1. **Implement MCP Server** - Basic session/message tools
2. **Add UI Monitoring** - Toast/message access
3. **Create Test Suite** - Replace file-based tests
4. **Integrate with CI** - GitHub Actions support

## Open Questions

1. Should MCP server be separate process or OpenCode plugin?
2. stdio vs WebSocket transport for tests?
3. How to handle authentication/security?

**This MCP approach would enable true E2E testing!**
