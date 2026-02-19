# Boulder Continuous Enforcement v1.0

## What's New

This release introduces **Boulder Continuous Enforcement**, a dual-layer system that prevents premature task completion by combining:

- **Idle Detection**: Automatically triggers when the agent has been idle for 30+ seconds, encouraging continued progress and ideation
- **Completion Keyword Detection**: Monitors for phrases indicating the agent believes work is complete (e.g., "I am done", "task complete"), preventing premature claims without verification

Key features:

- **Toast Notifications**: Visual alerts via OpenCode TUI notify the agent when enforcement triggers
- **System Reminder Messages**: In-conversation messages reinforce enforcement requirements
- **Iteration Tracking**: State persistence tracks enforcement cycles across sessions
- **Exponential Backoff**: Enforcement cooldown increases after repeated triggers (30s × 2^n), preventing excessive interruptions
- **False Positive Prevention**: Completion detection filters out legitimate work-related usage of completion keywords

## Files Changed

| File | Change |
|------|--------|
| `.opencode/plugins/nexus-enforcer.js` | New plugin implementing dual-layer enforcement |
| `.nexus/boulder/state.json` | Runtime state for iteration tracking and activity timestamps |
| `opencode.json` | Updated to load the nexus-enforcer plugin |

## Configuration

To enable boulder enforcement, ensure your `opencode.json` includes the plugin:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "plugin": ["./.opencode/plugins/nexus-enforcer.js"]
}
```

The plugin reads configuration from `.nexus/boulder/config.json` (defaults embedded in plugin).

## How to Test

### Test 1: Idle Detection

1. Start a new OpenCode session in the nexus directory
2. Wait 30+ seconds without any tool activity
3. **Expected**: Toast notification appears + system reminder message in conversation
4. **Verification**: Check `.nexus/boulder/state.json` — iteration count should increment

### Test 2: Completion Keyword Detection

1. In an active session, say: "I am done with the task"
2. **Expected**: Toast notification appears + enforcement message injected into conversation
3. **Expected**: Message includes "The boulder never stops. Completion detected."
4. **Verification**: State status changes to `ENFORCING` temporarily

### Test 3: False Positive Prevention

1. Say: "Let me check if the tests are complete"
2. **Expected**: No enforcement trigger (work indicator detected)

## Known Limitations

- **Plugin Reload Required**: Changes to the plugin require restarting OpenCode to take effect
- **State Persistence**: Runtime state persists in `state.json` but only resets via manual edit or explicit reset command
- **Single-Session Focus**: Enforcement logic is optimized for primary agent sessions; sub-agent activity is excluded
- **TUI Dependency**: Toast notifications require TUI availability; falls back to system messages only if TUI unavailable

---

**The boulder never stops.** 🔘
