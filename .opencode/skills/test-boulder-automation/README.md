# Boulder Test Automation

Automated testing for the Nexus Boulder continuous enforcement system.

## Quick Start

### Option 1: Quick Local Test (Recommended)

Run this during a conversation to test the boulder:

```bash
.opencode/skills/test-boulder-automation/test-quick.sh
```

This will:
- Validate the plugin and state
- Monitor for 35 seconds
- Check if enforcement triggers

### Option 2: Full Server Test (Advanced)

**Prerequisites:**
- OpenCode Server running on port 8080
- Boulder plugin configured

```bash
.opencode/skills/test-boulder-automation/test.sh
```

This runs comprehensive tests:
1. Idle detection test
2. Cooldown verification
3. Activity reset test

## Manual Testing

During conversation, you can verify:

1. **Toast Notification** - Visual popup with countdown
2. **System Message** - Full reminder in chat
3. **Iteration Tracking** - Check state file
4. **Cooldown** - Wait 30s between enforcements

## Files

- `test-quick.sh` - Quick local verification
- `test.sh` - Full automated test suite
- `config.json` - Test configuration
- `SKILL.md` - Detailed documentation

## Using as OpenCode Skill

Add to your OpenCode config:

```json
{
  "skills": ["test-boulder-automation"]
}
```

Then run:

```
/test-boulder
```

## Troubleshooting

**Issue:** "State file not found"
- Solution: Boulder hasn't been initialized yet. Send a message first.

**Issue:** "Plugin has syntax errors"
- Solution: Check `.opencode/plugins/nexus-enforcer.js` for errors

**Issue:** Boulder not triggering
- Solution: Check cooldown status. May need to wait 30+ seconds since last enforcement.

## Current Status

The boulder is **operational** with:
- ✅ Idle detection (30s)
- ✅ System reminder messages
- ✅ Toast notifications  
- ✅ Cooldown enforcement
- ✅ Global lock (prevents duplicates)

**Latest Iteration:** 132