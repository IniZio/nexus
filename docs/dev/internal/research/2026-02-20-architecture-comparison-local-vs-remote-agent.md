# Architecture Comparison: Local Agent vs Remote Agent

**Date:** 2026-02-20  
**Context:** Nexus Agent Plugin Design  
**Goal:** Determine optimal approach for agent-workspace relationship

---

## Executive Summary

Two competing architectures for running AI agents with remote workspaces:

1. **Local Agent, Remote Workspace** (Sprites-inspired)
   - Agent runs on user's machine
   - Connects to remote workspace via API
   - Tools execute remotely, agent logic local

2. **Remote Agent, Local Tools** (Agent Proxy pattern)
   - Agent runs inside workspace
   - Local tools/auth forwarded via proxy
   - Agent has native workspace access

**Recommendation:** Hybrid approach - **Local Agent with Workspace SDK** for development, **Remote Agent with Proxy** for production isolation.

---

## Approach 1: Local Agent, Remote Workspace

### Concept (Sprites-Inspired)

The agent (OpenCode, Claude Code) runs on the user's local machine. The workspace runs remotely as a persistent environment. The agent connects to the workspace through a client SDK/CLI that provides:
- File system access (read/write)
- Command execution
- Tool execution in workspace context
- State synchronization

```
┌─────────────────────────────────────────────────────────────┐
│  LOCAL MACHINE (User's Laptop)                             │
│  ┌─────────────────────────────────────────────────────┐  │
│  │ OpenCode / Claude Code / Cursor                    │  │
│  │ (Full agent with AI models, config, auth)          │  │
│  └────────────────────┬────────────────────────────────┘  │
│                       │                                     │
│                       │ Nexus Workspace SDK               │
│                       │ (npm package: @nexus/workspace-sdk)│
│                       │                                    │
│  ┌────────────────────┴────────────────────────────────┐  │
│  │ Local Tool Registry                                │  │
│  │ - File operations → forwarded to workspace        │  │
│  │ - Shell commands → executed in workspace          │  │
│  │ - Git operations → can be local or remote         │  │
│  └────────────────────┬────────────────────────────────┘  │
└───────────────────────┼─────────────────────────────────────┘
                        │
                        │ Network Connection
                        │ (WebSocket / HTTP / gRPC)
                        │
┌───────────────────────┼─────────────────────────────────────┐
│  REMOTE (Cloud/Server)│                                    │
│  ┌────────────────────┴────────────────────────────────┐  │
│  │ Nexus Workspace Daemon                             │  │
│  │ - Receives commands from SDK                       │  │
│  │ - Executes in workspace context                    │  │
│  │ - Returns results to SDK                           │  │
│  └────────────────────┬────────────────────────────────┘  │
│                       │                                     │
│  ┌────────────────────┴────────────────────────────────┐  │
│  │ Workspace Environment                              │  │
│  │ - Filesystem (persistent)                          │  │
│  │ - Running processes                                │  │
│  │ - Installed tools                                  │  │
│  │ - Git repository                                   │  │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

### How It Works

1. **User runs agent locally:**
   ```bash
   opencode
   # or
   claude
   ```

2. **Agent loads Nexus Workspace SDK:**
   ```typescript
   import { WorkspaceClient } from '@nexus/workspace-sdk';
   
   const workspace = new WorkspaceClient({
     endpoint: 'wss://workspace.nexus.dev/my-workspace',
     token: process.env.NEXUS_TOKEN
   });
   ```

3. **Agent uses SDK instead of local fs/exec:**
   ```typescript
   // Instead of: fs.readFileSync('./src/index.ts')
   const content = await workspace.fs.readFile('./src/index.ts');
   
   // Instead of: execSync('npm test')
   const result = await workspace.exec('npm test');
   ```

4. **SDK communicates with remote workspace daemon:**
   - WebSocket for real-time bidirectional
   - HTTP for simple request/response
   - gRPC for structured operations

### Advantages

| Benefit | Explanation |
|---------|-------------|
| **Agent Native UX** | Agent runs exactly as user expects, just different backend |
| **No Auth Forwarding** | Auth stays local, only commands/data cross network |
| **Fast Iteration** | Change SDK, test immediately, no deployment |
| **Works with ALL agents** | Claude, OpenCode, Cursor, Aider, etc. |
| **Offline Capability** | SDK can queue commands, sync when online |
| **Debugging** | Easy to debug agent locally, view logs |

### Disadvantages

| Challenge | Explanation |
|-----------|-------------|
| **SDK Overhead** | Must wrap ALL tool calls (read, write, exec, etc.) |
| **Latency** | Every file operation = network round-trip |
| **Complexity** | Need SDK for each language (TS, Python, Go, etc.) |
| **Agent Modification** | May need agent-specific plugins/adapters |
| **Large Files** | Transferring big files over network is slow |

### Sprites Analysis

Sprites takes a similar but simpler approach:
- CLI tool (`sprite exec`) runs commands in remote Sprite
- No SDK, just CLI wrapper
- Focus on command execution, not agent integration
- HTTP access for web services

**Key Insight:** Sprites is lower-level infrastructure. Nexus would build the SDK layer on top of similar infrastructure.

---

## Approach 2: Remote Agent, Local Tools (Agent Proxy)

### Concept (Traditional Proxy Pattern)

The agent runs INSIDE the remote workspace. Local machine runs a lightweight proxy that:
- Forwards tool calls from workspace to local machine
- Provides local auth tokens, config files
- Makes local resources available in workspace

```
┌─────────────────────────────────────────────────────────────┐
│  LOCAL MACHINE (User's Laptop)                             │
│  ┌─────────────────────────────────────────────────────┐  │
│  │ Agent Proxy (Lightweight forwarder)                │  │
│  │ - Receives forwarded calls from workspace          │  │
│  │ - Executes using local tools                       │  │
│  │ - Returns results to workspace                     │  │
│  └────────────────────┬────────────────────────────────┘  │
│                       │                                     │
│  ┌────────────────────┴────────────────────────────────┐  │
│  │ Local Resources                                    │  │
│  │ - OpenCode/Claude config (~/.opencode)            │  │
│  │ - Auth tokens (1Password, API keys)               │  │
│  │ - SSH keys (~/.ssh)                               │  │
│  │ - Git credentials                                  │  │
│  │ - Custom tools                                     │  │
│  └────────────────────┬────────────────────────────────┘  │
└───────────────────────┼─────────────────────────────────────┘
                        │
                        │ Reverse Tunnel
                        │ (WebSocket / gRPC)
                        │
┌───────────────────────┼─────────────────────────────────────┐
│  REMOTE (Cloud/Server)│                                    │
│  ┌────────────────────┴────────────────────────────────┐  │
│  │ OpenCode / Claude Code / Agent                     │  │
│  │ (Running INSIDE workspace)                         │  │
│  │ - Has native workspace access                      │  │
│  │ - Calls local tools through proxy                  │  │
│  └────────────────────┬────────────────────────────────┘  │
│                       │                                     │
│  ┌────────────────────┴────────────────────────────────┐  │
│  │ Workspace Environment                              │  │
│  │ - Filesystem (native access)                       │  │
│  │ - Processes                                        │  │
│  │ - Tools (both local via proxy + workspace native) │  │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

### How It Works

1. **Workspace starts with agent pre-installed:**
   ```bash
   docker run nexus-workspace:latest
   # Contains: opencode, node, git, etc.
   ```

2. **Agent starts, connects to local proxy:**
   ```bash
   # Inside workspace
   opencode --proxy wss://user.local:8080
   ```

3. **Proxy runs on local machine:**
   ```bash
   nexus-agent-proxy --workspace wss://workspace.remote:443
   ```

4. **Agent uses tools, some forwarded:**
   ```typescript
   // File operations - NATIVE (fast, workspace filesystem)
   fs.readFile('./src/index.ts');
   
   // Auth token - FORWARDED to local proxy
   const token = await proxy.tools.getAuthToken('openai');
   
   // Git push - FORWARDED to use local SSH keys
   await proxy.tools.git.push();
   ```

### Advantages

| Benefit | Explanation |
|---------|-------------|
| **Zero SDK** | No SDK needed, agent runs natively in workspace |
| **Native Performance** | File operations are local to workspace (fast) |
| **Workspace Isolation** | Full environment control, reproducible |
| **Simple Model** | Agent is "in the workspace", period |
| **Standard Tools** | Use standard tool APIs, just some forwarded |

### Disadvantages

| Challenge | Explanation |
|-----------|-------------|
| **Auth Complexity** | Must forward auth tokens securely |
| **Requires Proxy** | Must run proxy process locally |
| **Latency for Local Tools** | Every forwarded call = network hop |
| **Setup Complexity** | User must start proxy + connect workspace |
| **Limited Agents** | Each agent needs proxy support |

---

## Detailed Comparison

### Performance

| Operation | Local Agent + SDK | Remote Agent + Proxy | Winner |
|-----------|------------------|---------------------|--------|
| Read small file | ~50ms (network) | ~1ms (local) | Remote Agent |
| Read large file | Slow (transfer) | Fast (local) | Remote Agent |
| Write file | ~50ms (network) | ~1ms (local) | Remote Agent |
| Git commit | ~50ms | ~1ms | Remote Agent |
| API call with auth | Local (fast) | ~50ms (forwarded) | Local Agent |
| Linter/formatter | Depends | Workspace native | Tie |

### Complexity

| Aspect | Local Agent + SDK | Remote Agent + Proxy | Winner |
|--------|------------------|---------------------|--------|
| Implementation | High (SDK for each lang) | Medium (proxy protocol) | Remote Agent |
| User Setup | Low (just SDK config) | Medium (start proxy) | Local Agent |
| Agent Compatibility | High (all agents) | Low (agent needs proxy support) | Local Agent |
| Auth Handling | Simple (local) | Complex (forwarding) | Local Agent |
| Debugging | Easy | Harder | Local Agent |

### Use Case Fit

| Scenario | Best Approach | Why |
|----------|--------------|-----|
| **Development** | Local Agent + SDK | Fast iteration, easy debugging |
| **CI/CD** | Remote Agent + Proxy | Isolated, reproducible |
| **Team Sharing** | Remote Agent + Proxy | Consistent environment |
| **Multi-workspace** | Local Agent + SDK | One agent, many workspaces |
| **Security Critical** | Remote Agent + Proxy | Workspace isolated |

---

## Hybrid Recommendation

### Phase 1: Local Agent with Workspace SDK (MVP)

**Why:**
- Faster to implement (single SDK vs proxy protocol)
- Works with existing agents immediately
- Easier to debug and iterate
- Users already comfortable with local agents

**Implementation:**
```typescript
// @nexus/workspace-sdk
import { WorkspaceClient } from '@nexus/workspace-sdk';

const client = new WorkspaceClient({
  endpoint: process.env.NEXUS_WORKSPACE_URL,
  auth: process.env.NEXUS_TOKEN
});

// Wraps fs, child_process, etc.
export const workspace = {
  fs: {
    readFile: (path) => client.fs.readFile(path),
    writeFile: (path, data) => client.fs.writeFile(path, data),
    // ... etc
  },
  exec: (command) => client.exec(command),
  // ... etc
};
```

**Usage in agents:**
```typescript
// OpenCode plugin
import { workspace } from '@nexus/workspace-sdk';

export default {
  "tool.execute.before": async (input) => {
    // Intercept file operations, redirect to workspace
    if (input.tool === 'read') {
      return workspace.fs.readFile(input.args.path);
    }
  }
};
```

### Phase 2: Remote Agent with Proxy (Advanced)

**Why:**
- Full workspace isolation
- Better for production/team use
- Native performance

**When:**
- After SDK proves the model
- When users need isolated environments
- For CI/CD scenarios

---

## Key Decision Factors

### Choose Local Agent + SDK if:
- ✅ You want to support ALL agents quickly
- ✅ Development velocity is priority
- ✅ Users comfortable with local agent setup
- ✅ Debugging agent behavior is important
- ✅ Network latency is acceptable (50-100ms)

### Choose Remote Agent + Proxy if:
- ✅ Workspace isolation is critical
- ✅ File I/O performance is paramount
- ✅ Consistent environment across users
- ✅ CI/CD automation is primary use case
- ✅ Security boundary at workspace level

---

## Implementation Priority

**Recommendation:** Start with **Local Agent + SDK** approach because:

1. **Sprites validation:** Sprites uses similar model successfully
2. **Incremental:** Can add Remote Agent later as optimization
3. **Flexibility:** SDK works with all agents, no proxy needed
4. **Debugging:** Much easier to develop and troubleshoot
5. **User familiarity:** Agent runs where users expect

**Migration path:** SDK can evolve to support both modes:
```typescript
// SDK detects mode automatically
const workspace = new WorkspaceClient({
  mode: 'auto', // 'local', 'remote', or 'auto'
  // Uses local fs if workspace mounted locally
  // Uses network SDK if workspace remote
});
```

---

## Open Questions

1. **File Watching:** How to watch files for changes in SDK approach?
2. **Large Binaries:** Transfer node_modules or use remote npm?
3. **Interactive Tools:** TUI tools (vim, htop) over SDK?
4. **Network Partition:** How does SDK handle disconnections?

---

## References

- [Sprites Documentation](https://docs.sprites.dev/) - Cloud VM approach
- [GitHub Codespaces](https://docs.github.com/en/codespaces) - Remote dev environment
- [VS Code Remote](https://code.visualstudio.com/docs/remote/remote-overview) - Local IDE, remote workspace
- [Project IDX](https://idx.dev/) - Cloud-based development

---

**Conclusion:** Local Agent with Workspace SDK offers the best balance of compatibility, development velocity, and user experience for Phase 1. Remote Agent with Proxy is a valuable optimization for Phase 2 when isolation and performance become critical.
