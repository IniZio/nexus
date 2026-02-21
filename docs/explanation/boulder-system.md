# Boulder System

The Boulder is an infinite continuous enforcement system that ensures AI agents never stop improving. It's the core mechanism that drives Nexus Enforcer.

## Core Concept

The Boulder represents the idea that improvement never ends. When an agent attempts to complete work, the Boulder blocks completion and forces generation of new tasks.

```
BOULDER ENFORCEMENT: You attempted completion. NOT ALLOWED.

The boulder NEVER stops. Generate new ideas NOW:
1. <task-1>
2. <task-2>
3. <task-3>

Iteration: 5 | Tasks completed: 12 | In queue: 8
Status: FORCED CONTINUATION

DO NOT STOP. Execute these tasks. Report your progress.
```

## How It Works

### 1. Idle Detection

The Boulder monitors agent activity and detects when an agent becomes idle:

```typescript
class BoulderIdleDetector {
  private lastActivityTime: number;
  private idleThresholdMs: number;
  
  checkIdle(): { isIdle: boolean; idleTime: number } {
    const idleTime = Date.now() - this.lastActivityTime;
    return {
      isIdle: idleTime > this.idleThresholdMs,
      idleTime
    };
  }
}
```

### 2. Completion Attempt Detection

The Boulder detects when an agent attempts to claim completion:

- Detects phrases like "I'm done", "complete", "finished"
- Blocks the completion
- Triggers enforcement

### 3. Task Queue

The Boulder maintains a queue of pending tasks:

```typescript
interface BoulderTask {
  id: string;
  description: string;
  status: 'pending' | 'active' | 'done';
  priority: number;
  category: string;
}
```

### 4. Enforcement

When triggered, the Boulder:
- Increments the iteration counter
- Ensures minimum tasks in queue
- Generates enforcement prompt
- Forces continuation

## Dual-Layer Enforcer

The Boulder uses a dual-layer approach:

### Passive Layer

- Monitors and logs agent activity
- Does not block actions
- Records all events

### Active Layer

- Enforces rules and blocks completion
- Triggers Boulder enforcement
- Generates prompts

## Configuration

| Setting | Description | Default |
|---------|-------------|---------|
| `minTasksInQueue` | Minimum tasks to maintain | 5 |
| `idleThresholdMs` | Idle time before enforcement | 60000ms |
| `nextTasksCount` | Tasks to show in prompts | 3 |

## State Management

The Boulder maintains state in `.nexus/boulder/`:

```typescript
interface BoulderState {
  iteration: number;           // Current iteration
  sessionStartTime: number;    // When session started
  totalWorkTimeMs: number;     // Total work time
  tasksCompleted: number;      // Tasks completed
  tasksCreated: number;        // Tasks created
  lastActivity: number;        // Last activity timestamp
  status: string;              // Current status
}
```

## Usage in Plugins

### OpenCode

```typescript
import { createBoulderEnforcer } from 'nexus-enforcer/boulder';

const enforcer = createBoulderEnforcer();

// Record tool calls
enforcer.recordToolCall('Read');

// Check for completion attempts
const result = enforcer.recordTextOutput("I'm done");
if (result.enforce) {
  // Block and show enforcement message
  showEnforcementMessage(result.message);
}
```

### Claude

```typescript
import { getGlobalEnforcement } from 'nexus-enforcer/boulder';

const enforcement = getGlobalEnforcement();

// In hook
hooks.on('text', (text) => {
  const result = enforcement.recordTextOutput(text);
  if (result.enforce) {
    return { content: result.message, blocked: true };
  }
});
```

## Statistics

The Boulder tracks:

- **Iteration**: Number of enforcement cycles
- **Tasks Created**: Total tasks generated
- **Tasks Completed**: Tasks marked as done
- **Work Time**: Total active work time
- **Idle Time**: Time spent idle

View with `boulder status`.

## Philosophy

The Boulder embodies the principle that:

1. **No completion is final** - There's always room for improvement
2. **Idle is not allowed** - Continuous work is mandatory
3. **Enforcement is automatic** - No manual intervention needed

This ensures autonomous agents maintain high productivity and never settle for "good enough".
