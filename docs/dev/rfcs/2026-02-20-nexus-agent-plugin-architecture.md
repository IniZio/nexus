# Nexus Agent Plugin Architecture - RFC

**Status:** Draft  
**Created:** 2026-02-20  
**Supersedes:** [REMOVED] Initial file-based design (2026-02-20-nexus-opencode-plugin.md)

---

## 1. Abstract

This document describes the architecture for the Nexus Agent Plugin - an agent-agnostic system for continuous enforcement, task management, and workspace orchestration. The system ensures AI agents only stop when user intervention is genuinely required, maintains context quality during compaction, and supports remote workspace execution.

**Key Innovation:** Clear separation of concerns between Node, Coordination, Workspace State, and Snapshot components, enabling future remote workspace execution while maintaining local development ergonomics.

---

## 2. Terminology

- **Boulder** - Internal codename for the continuous iteration concept (NOT a user-facing mode)
- **Enforcer** - Component that prevents premature agent stopping
- **Node** - The user's local machine or a remote server
- **Coordination** - Central orchestration layer (can be local or remote)
- **Workspace State** - Live runtime state of a workspace
- **Workspace Snapshot** - Serializable checkpoint of workspace at a point in time
- **Context Compaction** - Process of summarizing/slimming conversation history

---

## 3. Architecture Overview

### 3.1 Component Separation

```
┌─────────────────────────────────────────────────────────────┐
│                        USER LAYER                          │
│  (Local machine with OpenCode/Claude/Cursor/Aider/etc.)    │
└──────────────────────┬──────────────────────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────────────────────┐
│                      NODE LAYER                            │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────────┐ │
│  │ Agent Client │  │ Agent Config │  │ Auth Credentials │ │
│  │ (Proxy/Stub) │  │ (Synced)     │  │ (Synced)         │ │
│  └──────────────┘  └──────────────┘  └──────────────────┘ │
└──────────────────────┬──────────────────────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────────────────────┐
│                   COORDINATION LAYER                       │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────────┐ │
│  │ Enforcer     │  │ Task Queue   │  │ State Sync       │ │
│  │ Engine       │  │ Manager      │  │ Service          │ │
│  └──────────────┘  └──────────────┘  └──────────────────┘ │
└──────────────────────┬──────────────────────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────────────────────┐
│                  WORKSPACE LAYER                           │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────────┐ │
│  │ Workspace    │  │ Workspace    │  │ Snapshot         │ │
│  │ State (Live) │  │ Snapshot     │  │ Storage          │ │
│  │              │  │ (Serialized) │  │ (Versioned)      │ │
│  └──────────────┘  └──────────────┘  └──────────────────┘ │
└─────────────────────────────────────────────────────────────┘
```

### 3.2 Component Responsibilities

**Node Layer:**
- Agent availability and configuration synchronization
- Local caching of coordination state
- Proxy/stub for agent tools when running remotely

**Coordination Layer:**
- Enforcer: Decides when agents should stop vs continue
- Task Queue: Priority-based task distribution
- State Sync: Synchronizes state between node and workspace

**Workspace Layer:**
- Workspace State: Live running state (ephemeral, in-memory)
- Workspace Snapshot: Serializable checkpoints for persistence

---

## 4. Detailed Design

### 4.1 Node Layer - Agent Availability Problem

**Problem:** Users have agents installed locally with configurations and auth. When workspace runs remotely, how do we make those agents available?

**Solution: Agent Proxy Pattern**

```
User's Local Machine                    Remote Workspace
┌─────────────────────┐               ┌─────────────────────┐
│  OpenCode/Claude    │◄─────────────►│  Nexus Agent Stub   │
│  (Full installation)│   WebSocket   │  (Lightweight)      │
│                     │   or gRPC     │                     │
│  ┌───────────────┐  │               │  ┌───────────────┐  │
│  │ Config Files  │  │               │  │ Proxied Tools │  │
│  │ ~/.opencode   │  │               │  │ (read/write)  │  │
│  └───────────────┘  │               │  └───────────────┘  │
│                     │               │                     │
│  ┌───────────────┐  │               │  ┌───────────────┐  │
│  │ Auth Tokens   │  │               │  │ Proxied Auth  │  │
│  │ API Keys      │  │               │  │ (forwarded)   │  │
│  └───────────────┘  │               │  └───────────────┘  │
└─────────────────────┘               └─────────────────────┘
```

**Implementation:**

1. **Agent Proxy** runs on user's local machine
2. **Agent Stub** runs in remote workspace (minimal, just forwards calls)
3. **Bidirectional sync** of:
   - Configuration files (`.opencode/config.json`, `.cursorrules`, etc.)
   - Authentication tokens (with user consent)
   - Tool capabilities

**Configuration Sync Strategy:**

```typescript
interface AgentConfig {
  // Core config that moves with workspace
  type: 'opencode' | 'claude' | 'cursor' | 'aider';
  version: string;
  
  // User-specific (stays on node)
  userConfig: {
    apiKeys: Encrypted<KeyMap>;
    preferences: UserPreferences;
  };
  
  // Workspace-specific (moves to workspace)
  workspaceConfig: {
    plugins: string[];
    commands: Record<string, CommandDef>;
    rules: string[];  // AGENTS.md content
  };
}
```

### 4.2 Coordination Layer - The Enforcer

**Core Responsibility:** Ensure agents only stop when user intervention is required.

**Enforcement Decision Flow:**

```
Agent attempts to stop/complete
         │
         ▼
┌──────────────────────┐
│ 1. Check Task Status │
│    - All acceptance  │
│      criteria met?   │
└──────────┬───────────┘
           │
      No   │   Yes
           ▼
┌──────────────────────┐
│ 2. Check Quality     │
│    - Tests pass?     │
│    - Build succeeds? │
│    - Type checks?    │
└──────────┬───────────┘
           │
      No   │   Yes
           ▼
┌──────────────────────┐
│ 3. Check Context     │
│    - Conversation    │
│      length?         │
│    - Need compaction?│
└──────────┬───────────┘
           │
      Yes  │   No
           ▼
┌──────────────────────┐
│ 4. Compaction Needed │
│    - Summarize work  │
│    - Create snapshot │
│    - Prompt continue │
└──────────┬───────────┘
           │
           ▼
┌──────────────────────┐
│ ALLOW STOP           │
│ or                   │
│ ENFORCE CONTINUE     │
└──────────────────────┘
```

**Enforcer Interface:**

```go
type Enforcer interface {
  // Check if agent should be allowed to stop
  ShouldAllowStop(ctx context.Context, req StopRequest) (*StopDecision, error)
  
  // Get current enforcement status
  GetStatus(ctx context.Context) (*EnforcementStatus, error)
  
  // Request user intervention
  RequestIntervention(ctx context.Context, reason string) error
}

type StopRequest struct {
  TaskID      string
  WorkspaceID string
  AgentType   string
  Iteration   int
  AttemptedCompletion bool
}

type StopDecision struct {
  Allowed bool
  Reason  string
  Actions []EnforcementAction
}

type EnforcementAction struct {
  Type    string // "continue", "intervention", "compaction"
  Message string
  Metadata map[string]interface{}
}
```

### 4.3 Context Compaction Quality

**Problem:** Agents lose important context when conversations get too long and are summarized.

**Solution: Structured Compaction with Quality Gates**

```typescript
interface CompactionStrategy {
  // When to compact (token count, message count, etc.)
  trigger: CompactionTrigger;
  
  // What to preserve
  preserve: {
    criticalDecisions: boolean;  // Key architectural decisions
    acceptanceCriteria: boolean; // Task completion requirements
    activeFiles: boolean;        // Currently open/edited files
    testResults: boolean;        // Recent test outcomes
    errors: boolean;             // Unresolved errors
  };
  
  // Quality check after compaction
  qualityGate: {
    minInformationRetention: number; // 0.0 - 1.0
    requiredContext: string[];       // Must be present in summary
  };
}

// Compaction Process
async function compactContext(
  conversation: Conversation,
  strategy: CompactionStrategy
): Promise<CompactionResult> {
  // 1. Extract critical information
  const critical = extractCriticalInfo(conversation, strategy.preserve);
  
  // 2. Summarize non-critical portions
  const summary = await summarize(conversation, strategy.trigger);
  
  // 3. Merge critical + summary
  const compacted = merge(critical, summary);
  
  // 4. Quality gate check
  const quality = await assessQuality(compacted, strategy.qualityGate);
  
  if (!quality.passed) {
    // Retry with different strategy or request user intervention
    return {
      success: false,
      reason: quality.failures,
      recommendedAction: "user_intervention"
    };
  }
  
  return {
    success: true,
    compactedContext: compacted,
    metrics: quality.metrics
  };
}
```

### 4.4 Workspace State vs Snapshot

**Workspace State (Live):**
- Ephemeral, in-memory
- Runtime information (processes, file handles)
- Active conversation state
- Current tool outputs

**Workspace Snapshot (Persistent):**
- Serializable checkpoint
- Full file system state
- Conversation history (compacted)
- Task progress
- Can be moved, backed up, restored

```go
type WorkspaceState struct {
  ID        string
  Status    WorkspaceStatus // running, paused, stopped
  
  // Runtime state (not serialized)
  Processes []ProcessInfo
  FileHandles []FileHandle
  ActiveConversation *Conversation
  
  // Reference to snapshot
  CurrentSnapshotID string
}

type WorkspaceSnapshot struct {
  ID          string
  CreatedAt   time.Time
  WorkspaceID string
  
  // Serialized state
  FileSystem    FileSystemSnapshot
  Conversation  CompactedConversation
  Tasks         []Task
  EnforcerState EnforcerCheckpoint
  
  // Metadata
  Metrics SnapshotMetrics
}
```

---

## 5. Data Flow Examples

### 5.1 Local Development (Single Node)

```
User runs: opencode
    │
    ▼
Node Layer: Agent runs locally
    │
    ▼
Coordination: Embedded (same process)
    │
    ▼
Workspace: Local filesystem
    │
    ▼
State: Local SQLite or files
```

### 5.2 Remote Workspace

```
User runs: opencode (connects to remote)
    │
    ▼
Node Layer: Agent Proxy on local machine
    │ forwards calls
    ▼
Coordination: Remote server
    │
    ▼
Workspace: Remote container/VM
    │
    ▼
State: Remote database
    │
    ▼
Snapshot: Object storage (S3, etc.)
```

---

## 6. Storage Strategy

**NOT file-based** - clear separation:

| Component | Storage | Reason |
|-----------|---------|--------|
| Coordination State | PostgreSQL/etcd | Consistency, replication |
| Workspace State | In-memory + WAL | Performance, crash recovery |
| Workspace Snapshot | Object Storage (S3) | Durability, versioning |
| Node Cache | Local SQLite | Offline resilience |
| Agent Config | Git-backed | Version control |

---

## 7. Implementation Phases

### Phase 1: Core Coordination (Week 1-2)
- [ ] Enforcer engine with quality gates
- [ ] Context compaction with quality checks
- [ ] In-memory workspace state
- [ ] SQLite coordination for single-node

### Phase 2: Agent Proxy (Week 3-4)
- [ ] Agent proxy/stub protocol
- [ ] Config synchronization
- [ ] Auth forwarding (secure)
- [ ] Local agent availability

### Phase 3: Remote Ready (Week 5-6)
- [ ] Remote coordination server
- [ ] Workspace snapshot/restore
- [ ] Object storage integration
- [ ] Network protocols (WebSocket/gRPC)

### Phase 4: Multi-Agent (Week 7-8)
- [ ] OpenCode adapter
- [ ] Claude Code adapter
- [ ] Cursor adapter
- [ ] Aider adapter

---

## 8. Open Questions

1. **Auth Forwarding Security**: How to securely forward auth tokens without exposing them?
2. **Offline Mode**: How should the system behave when node loses connection to remote coordination?
3. **Multi-User**: How to handle multiple users working on same workspace?

---

## 9. References

- [athal7/opencode-devcontainers](https://github.com/athal7/opencode-devcontainers) - Inspiration for remote workspace patterns
- nexus-old/ - Previous attempt lessons learned
- AGENTS.md - User-facing guidelines
