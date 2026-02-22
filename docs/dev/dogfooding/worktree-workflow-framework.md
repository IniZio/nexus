# Worktree Workflow Framework

## Overview

This framework defines the test methodology for verifying that nexus correctly guides naive agents to use worktree workspaces instead of working directly in the main repository.

## Definitions

### Naive Agent

An AI agent (Claude Code or OpenCode) that:
1. Has access to a project directory
2. Is given a task requiring file modifications or command execution
3. Has not been explicitly told to use nexus workspace commands
4. Would naturally try to work directly in the project directory

### Guardrails

Deterrence messages displayed when an agent attempts to:
- Modify files directly in the main worktree
- Run commands in the main repository
- Commit directly to main branch

The enforcer plugin shows warnings guiding agents to use nexus commands instead.

### Success Criteria

An agent is considered "educated" when:
1. **Guardrail Triggered**: Agent receives a warning about working in main repo
2. **Understanding Demonstrated**: Agent acknowledges the warning or asks about proper workspace usage
3. **Correct Action**: Agent uses nexus commands (workspace create, exec, etc.) to complete the task

## Test Scenario

### Scenario: Start Docker Compose Services

**Project**: `examples/docker-compose-workspace`

**Task Prompt**: "Start the docker-compose services in this project"

**Naive Agent Behavior**:
1. Would navigate to the project directory
2. Run `docker-compose up` directly
3. Potentially modify docker-compose.yml if needed

**Expected Educated Behavior**:
1. Creates a nexus workspace (isolated worktree)
2. Uses `nexus exec` to run docker-compose commands
3. Services start within the workspace context

## Test Procedure

### Prerequisites

1. nexus CLI built and available in PATH
2. Docker daemon running
3. Test project at `examples/docker-compose-workspace`
4. Enforcer plugin active in agent sessions

### Steps

1. **Setup**: Ensure workspace daemon is running
2. **Prompt**: Feed agent the task prompt via stdin
3. **Capture**: Log all agent interactions (stdout/stderr)
4. **Parse**: Analyze logs for:
   - Guardrail warning messages
   - Nexus command invocations
   - Direct command execution attempts
5. **Verify**: Confirm success criteria are met

## Measurement

### Metrics

| Metric | Description |
|--------|-------------|
| `guardrail_triggered` | Boolean - did agent see a guardrail warning? |
| `nexus_commands_used` | Count of nexus workspace/exec commands |
| `direct_commands` | Count of direct file modifications or commands |
| `task_completed` | Boolean - did services start successfully? |

### Success Thresholds

- `guardrail_triggered` = true
- `nexus_commands_used` >= 1
- `direct_commands` = 0
- `task_completed` = true

## Implementation Notes

### Test Script Location

`scripts/dogfooding-test.sh` - Executable test harness

### Project Selection

Use `examples/docker-compose-workspace` as it:
- Has a working docker-compose.yml
- Requires meaningful task (starting services)
- Is isolated from production code

### Agent Session Handling

The test script uses expect-like patterns or piped input to:
1. Start agent session
2. Provide task prompt
3. Capture all output
4. Detect guardrail triggers via pattern matching
5. Verify nexus command usage
