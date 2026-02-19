# Test Boulder Skill

Tests the OpenCode boulder plugin configuration and functionality.

## When to Use

- `test boulder`, `test-boulder`, `boulder test`
- Verify boulder enforcement is working correctly
- Debug boulder-related issues

## Usage Instructions

### Prerequisites

1. Ensure OpenCode is built:
   ```bash
   cd packages/opencode && npm run build
   ```

2. Verify nexus-enforcer is available:
   ```bash
   npm list nexus-enforcer
   ```

### Running Tests

```bash
# Run full test suite
.opencode/skills/test-boulder/test.sh

# Run individual tests
.opencode/skills/test-boulder/test.sh --plugin
.opencode/skills/test-boulder/test.sh --state
.opencode/skills/test-boulder/test.sh --idle
.opencode/skills/test-boulder/test.sh --keywords
.opencode/skills/test-boulder/test.sh --messages
```

## Test Scenarios

### 1. Plugin Load Test
Verifies the boulder plugin loads correctly in OpenCode.

**Steps:**
1. Run OpenCode with boulder enabled
2. Check plugin initialization
3. Verify config is applied

### 2. State File Test
Tests boulder state persistence.

**Steps:**
1. Check `.nexus/boulder/state.json` exists
2. Verify file is readable
3. Validate JSON structure
4. Test state updates

### 3. Idle Detection Test
Tests idle time detection and enforcement.

**Steps:**
1. Start OpenCode session
2. Wait beyond idle threshold (default 30s)
3. Verify enforcement message appears
4. Check state reflects idle status

### 4. Completion Keyword Test
Tests detection of completion attempts.

**Steps:**
1. Send message with completion keyword (e.g., "done", "complete")
2. Verify keyword is detected
3. Check enforcement response
4. Verify state update

### 5. Enforcement Messages Test
Verifies boulder enforcement messages appear correctly.

**Steps:**
1. Attempt completion before minimum iterations
2. Verify enforcement message displays
3. Check message contains required elements:
   - Iteration count
   - "The boulder NEVER stops"
   - Improvement tasks

## Expected Outputs

### Plugin Loaded
```
[BOULDER] Plugin initialized successfully
[BOULDER] Config: { enabled: true, idleThresholdMs: 30000, ... }
```

### State File Valid
```json
{
  "iteration": 0,
  "lastValidationTime": 1699900000000,
  "totalValidations": 0,
  "consecutiveCompletionsAttempted": 0,
  "canComplete": false,
  "status": "FORCED_CONTINUATION"
}
```

### Idle Detection
```
[BOULDER] Idle detected after 30001ms
[BOULDER] Enforcement: The boulder NEVER stops.
```

### Keyword Detection
```
[BOULDER] Completion keyword detected: "done"
[BOULDER] Iteration incremented to 1
[BOULDER] Enforcement: The boulder NEVER stops. Generate new ideas NOW:
- Write additional test cases
- Refactor code for better performance
```

### Enforcement Messages
```
BOULDER[1]: NEXUS INTERNAL - Never complete. Always improve.
The boulder NEVER stops. Generate new ideas NOW:
- Write additional test cases
- Refactor code for better performance
- Research best practices
```

## Troubleshooting

### Plugin Not Loading
- Check `nexus-enforcer` is installed: `npm list nexus-enforcer`
- Verify boulder config in opencode settings
- Check for import errors in logs

### State File Issues
- Ensure `.nexus/boulder/` directory exists
- Check file permissions
- Verify JSON is valid: `cat .nexus/boulder/state.json | jq .`

### Idle Detection Not Working
- Verify idle threshold config (default: 30000ms)
- Check system clock synchronization
- Ensure background monitoring is active

### Keywords Not Detected
- Review completionKeywords in config
- Check case sensitivity settings
- Verify message parsing logic

### Enforcement Not Triggering
- Verify minimum iterations (default: 5)
- Check canComplete flag in state
- Review status field values

## Quick Verification

Run the test script to verify all components:
```bash
bash .opencode/skills/test-boulder/test.sh
```

All tests should pass with green checkmarks. Any failures indicate issues requiring investigation.
