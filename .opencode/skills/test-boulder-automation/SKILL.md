# Boulder Test Automation Skill

Automated testing of the Nexus Boulder continuous enforcement using OpenCode Server.

## Purpose

This skill starts OpenCode in server mode and runs automated tests to verify:
1. Boulder plugin loads correctly
2. Idle detection triggers after 30 seconds
3. System messages appear in conversation
4. Cooldown prevents rapid re-triggering
5. Toast notifications work

## Prerequisites

- OpenCode CLI installed
- Boulder plugin configured in `.opencode/plugins/`
- State file at `.nexus/boulder/state.json`

## Usage

Run the automated test suite:

```bash
.opencode/skills/test-boulder-automation/test.sh
```

Or use the skill directly:

```
/test-boulder
```

## Test Flow

1. **Setup Phase**
   - Reset boulder state (iteration: 0)
   - Start OpenCode server
   - Initialize test session

2. **Test Phase 1: Idle Detection**
   - Send initial message
   - Wait 35 seconds (30s idle + 5s buffer)
   - Verify enforcement triggered
   - Check iteration incremented
   - Verify system message appeared

3. **Test Phase 2: Cooldown**
   - Wait 20 seconds (should NOT trigger)
   - Verify no enforcement
   - Wait another 15 seconds (35s total)
   - Verify second enforcement

4. **Test Phase 3: Activity Reset**
   - Send message during idle
   - Verify timer reset
   - Wait 30 seconds
   - Verify enforcement after reset

5. **Cleanup Phase**
   - Stop OpenCode server
   - Generate test report
   - Restore state if needed

## Configuration

Edit `config.json` to customize:

```json
{
  "idleThresholdMs": 30000,
  "cooldownMs": 30000,
  "testTimeoutMs": 120000,
  "verboseLogging": true
}
```

## Integration with CI/CD

Add to GitHub Actions:

```yaml
- name: Test Boulder Enforcement
  run: |
    cd .opencode/skills/test-boulder-automation
    ./test.sh --ci-mode
```

## Troubleshooting

### Server Won't Start
- Check if port 8080 is available
- Verify OpenCode CLI is installed: `which opencode`
- Check logs: `cat .opencode/skills/test-boulder-automation/logs/server.log`

### Tests Failing
- Reset state manually: `echo '{"iteration":0,...}' > .nexus/boulder/state.json`
- Check plugin syntax: `node --check .opencode/plugins/nexus-enforcer.js`
- Review test logs: `cat .opencode/skills/test-boulder-automation/logs/test.log`

### Timeout Issues
- Increase `testTimeoutMs` in config
- Check system load during tests
- Verify OpenCode server is responsive

## Architecture

```
test-boulder-automation/
├── SKILL.md              # This file
├── test.sh               # Main test runner
├── server.sh             # Server management
├── config.json           # Test configuration
├── lib/
│   ├── test-runner.js    # Test orchestration
│   ├── assertions.js     # Test assertions
│   └── reporter.js       # Report generation
└── logs/                 # Test logs
```

## Success Criteria

✅ All tests pass when:
- Boulder triggers exactly once per 30s idle period
- System reminder message appears in conversation
- Cooldown prevents rapid re-triggering
- Toast notifications display correctly
- State persists across operations

## References

- OpenCode Server: https://opencode.ai/docs/server/
- Boulder Design: /docs/boulder/DESIGN.md
- Plugin API: https://opencode.ai/docs/plugins/