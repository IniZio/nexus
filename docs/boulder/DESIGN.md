# Boulder Continuous Enforcement - Design Document

## Overview

The Boulder is a continuous enforcement system for OpenCode that ensures agents never stop improving. It automatically detects idle periods and completion attempts, then forces the agent to continue working through system reminders and toast notifications.

## Architecture Philosophy

**Core Principle:** *The boulder never stops rolling.*

There is no true "completion" - only continuous iteration. The system enforces that agents must:
1. Verify their work (tests pass, build succeeds)
2. Address all requirements explicitly
3. Provide evidence of success
4. Continue improving indefinitely

## Implementation Patterns

### Pattern 1: Dual-Layer Enforcement

Inspired by oh-my-opencode's approach, we implement two complementary enforcement mechanisms:

#### Layer 1: Completion Detection (Primary)
- **Hook:** `experimental.chat.system.transform`
- **Trigger:** Detects completion keywords in AI responses
- **Keywords:** "done", "complete", "finished", "task complete", "i am done", etc.
- **False Positive Prevention:** Checks for work indicators ("tool", "implement", "working on")
- **Action:** Immediate enforcement with warning variant toast

#### Layer 2: Idle Detection (Fallback)
- **Hook:** `event` (listening for `session.idle`)
- **Trigger:** Session idle for 30+ seconds
- **Purpose:** Catches agents that try to slip through Layer 1
- **Action:** Enforcement with countdown and error variant toast

**Design Decision:** Two layers provide redundancy. Layer 1 catches explicit completion attempts, while Layer 2 ensures continuous work even if the agent doesn't explicitly state completion.

### Pattern 2: Decision Gates

Following oh-my-opencode's proven pattern, enforcement only triggers after passing multiple decision gates:

```
session.idle event received
    ↓
Is main agent? (not subagent)
    ↓
Is idle long enough? (30s threshold)
    ↓
Has cooldown passed? (30s × 2^failures)
    ↓
Below max failures? (5 max)
    ↓
Not stopped by user?
    ↓
TRIGGER ENFORCEMENT
```

**Gate Details:**

1. **Agent Check** - Only enforce on main agent, not subagents
   - Prevents interfering with delegated tasks
   - Allows parallel work to continue

2. **Idle Threshold** - Minimum 30 seconds of inactivity
   - Prevents triggering during normal work pauses
   - Balanced to catch genuine idleness without being overly aggressive

3. **Cooldown** - Exponential backoff between enforcements
   - Base: 30 seconds
   - Multiplier: 2^failureCount
   - Prevents spam when agent is genuinely stuck
   - Example: 30s, 60s, 120s, 240s, 480s

4. **Max Failures** - Stop after 5 consecutive failures
   - Prevents infinite loops
   - Resets after 5-minute recovery window

5. **Stop Flag** - User can request temporary stop
   - Cleared automatically on new activity
   - Respects user autonomy

### Pattern 3: State Persistence

**State File:** `.nexus/boulder/state.json`

```json
{
  "iteration": 2,
  "lastActivity": 1771559262783,
  "lastEnforcement": 1771559262484,
  "failureCount": 0,
  "stopRequested": false,
  "status": "CONTINUOUS"
}
```

**Rationale:**
- Survives OpenCode restarts
- Tracks cumulative progress
- Enables exponential backoff
- Provides visibility into enforcement history

### Pattern 4: Activity Tracking

**Mechanism:** Reset idle timer on every tool call and message

**Hooks:**
- `tool.execute.before` - Any tool usage resets timer
- `experimental.chat.system.transform` - Any message resets timer

**Purpose:** Ensure legitimate work doesn't trigger false enforcement

### Pattern 5: Message Injection

**Two-Channel Approach:**

1. **Toast Notification** (Visual)
   - Uses `client.tui.showToast()`
   - 15-second duration
   - Error variant (red) for idle, Warning variant (yellow) for completion
   - Visible but doesn't interrupt workflow

2. **System Message** (Conversation)
   - Uses `client.session.promptAsync()`
   - Injected as assistant message
   - Contains full enforcement text with requirements
   - Persistent in conversation history

**Design Decision:** Two channels ensure visibility. Toast provides immediate attention, while system message provides persistent reminder with full context.

### Pattern 6: False Positive Prevention

**Problem:** Agents often use completion words in legitimate contexts ("let me complete the function")

**Solution:** Dual-keyword checking

1. **Completion Keywords** - Must have one
   - "done", "complete", "finished", "i am done", etc.

2. **Work Indicators** - Must NOT have
   - "tool", "implement", "working on", "in progress", etc.

**Example:**
- "I am done" → ✅ Enforce (completion only)
- "Let me complete the function" → ❌ Skip (has work indicator)

## Comparison with Oh-My-OpenCode

### What We Adopted

| Feature | Oh-My-OpenCode | Our Implementation |
|---------|----------------|-------------------|
| Dual-layer enforcement | ✅ Todo + Atlas hooks | ✅ Completion + Idle hooks |
| Decision gates | ✅ 7+ gates | ✅ 5 gates (simplified) |
| Exponential backoff | ✅ 30s × 2^failures | ✅ Same |
| Max failures | ✅ 5 failures | ✅ Same |
| Cooldown | ✅ Yes | ✅ Same |
| Toast notifications | ✅ Yes | ✅ Same pattern |
| Message injection | ✅ Yes | ✅ Same pattern |
| State persistence | ✅ Yes | ✅ File-based |

### What We Simplified

| Feature | Oh-My-OpenCode | Our Implementation | Rationale |
|---------|----------------|-------------------|-----------|
| Todo checking | ✅ Fetch and check todos | ❌ Not implemented | Nexus doesn't use todo system |
| Background tasks | ✅ Check running tasks | ❌ Not implemented | Single-agent workflow |
| Abort detection | ✅ 3s abort window | ❌ Not implemented | Lower priority |
| Agent filtering | ✅ Skip prometheus/compaction | ❌ Not implemented | Single agent type |
| Countdown toast | ✅ 2s countdown | ❌ Direct toast | Simpler UX |
| Stability checks | ✅ 5s unchanged check | ❌ Not implemented | Lower priority |

**Rationale for Simplifications:**

1. **No Todo System** - Nexus uses a simpler workflow without explicit todo tracking
2. **Single Agent** - No background tasks or agent delegation to worry about
3. **Prioritization** - Abort detection and stability checks are nice-to-have but not critical
4. **Direct Toast** - Countdown adds complexity without significant benefit for our use case

## Configuration

### Plugin Configuration (`opencode.json`)

```json
{
  "$schema": "https://opencode.ai/config.json",
  "plugin": [
    "./.opencode/plugins/nexus-enforcer.js"
  ]
}
```

### Runtime Configuration (`.nexus/boulder/state.json`)

Auto-generated and managed by the plugin. Do not edit manually.

### Hardcoded Constants

```javascript
const CONFIG = {
  IDLE_THRESHOLD_MS: 30000,        // 30 seconds
  COOLDOWN_MS: 30000,              // 30 seconds base
  COUNTDOWN_SECONDS: 2,            // Warning time
  MAX_FAILURES: 5,                 // Max consecutive failures
  BACKOFF_MULTIPLIER: 2            // Exponential multiplier
};
```

## Testing

### Manual Testing Checklist

1. **Idle Detection**
   - Send message
   - Wait 30 seconds
   - Verify toast appears
   - Verify system message appears
   - Check iteration incremented

2. **Completion Detection**
   - Type "I am done"
   - Verify immediate enforcement
   - Check warning variant toast

3. **False Positive Prevention**
   - Type "Let me complete the function"
   - Verify NO enforcement

4. **Cooldown**
   - Trigger enforcement
   - Wait <30 seconds
   - Verify no second enforcement

5. **Activity Reset**
   - Send message during idle
   - Verify timer resets

### Automated Testing

Run the test skill:
```bash
.opencode/skills/test-boulder/test.sh
```

## Future Enhancements

### Short Term
1. Add abort detection (3s window)
2. Add stability checks (5s unchanged)
3. Add countdown toast (2s warning)

### Long Term
1. Integrate with Nexus todo system
2. Add background task awareness
3. Configurable thresholds via config file
4. Web dashboard for enforcement analytics

## References

1. **Oh-My-OpenCode Source:** `.opencode/oh-my-opencode-study/`
2. **Plugin Documentation:** https://opencode.ai/docs/plugins/
3. **Hook Reference:** OpenCode plugin API documentation
4. **Implementation:** `.opencode/plugins/nexus-enforcer.js`

## Changelog

### v1.0.0 - Initial Release
- Dual-layer enforcement (completion + idle)
- Toast notifications
- System message injection
- State persistence
- Exponential backoff
- False positive prevention

## License

MIT - Same as Nexus project
