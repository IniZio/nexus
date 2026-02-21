# Nexus Enforcer Testing Plan

## Overview

This document outlines how to test the nexus-enforcer plugin to ensure it properly enforces workspace usage, dogfooding, and task completion.

## Prerequisites

Before testing, ensure:
1. All packages are built: `task build`
2. Project-specific OpenCode config exists: `.opencode/opencode.json`
3. You're in the nexus directory

## Test 1: Plugin Loading

**Objective:** Verify the plugin loads correctly when OpenCode starts.

**Steps:**
1. Open a terminal in `/home/newman/magic/nexus-dev/nexus`
2. Run: `opencode --version`
3. Check OpenCode logs for plugin loading messages

**Expected Result:**
- OpenCode starts without errors
- Plugin "nexus-opencode" is loaded
- No 404 or module not found errors

## Test 2: Workspace Enforcement

**Objective:** Verify the plugin blocks operations outside nexus workspaces.

**Steps:**
1. Ensure you're in the main worktree (not a workspace)
2. Start OpenCode: `opencode`
3. Ask: "Create a new file called test.txt"

**Expected Result:**
- Plugin detects you're not in a workspace
- Injects workspace enforcement prompt
- Suggests: "Create a workspace first: nexus workspace create test"
- Operation is blocked or deferred

## Test 3: Dogfooding Verification

**Objective:** Verify dogfooding checks before task completion.

**Steps:**
1. Create a workspace: `nexus workspace create dogfood-test`
2. Enter workspace: `cd .nexus/worktrees/dogfood-test`
3. Start OpenCode in workspace
4. Complete a small task (e.g., "Add a comment to README")
5. Try to mark task as complete

**Expected Result:**
- Plugin checks for:
  - Friction log existence (`.nexus/dogfooding/friction-log.md`)
  - Workspace verification
  - Test/verification evidence
- If checks fail, prompts to complete dogfooding
- Blocks completion until verified

## Test 4: Task Completion Enforcement (Boulder Rolling)

**Objective:** Verify the plugin prevents stopping with incomplete todos.

**Steps:**
1. Create a workspace: `nexus workspace create boulder-test`
2. Enter workspace
3. Start OpenCode
4. Create a todo list:
   ```
   /todo write
   - [ ] Task 1: Create file A
   - [ ] Task 2: Create file B
   - [ ] Task 3: Verify both files
   ```
5. Complete only Task 1
6. Try to stop or say "I'm done"

**Expected Result:**
- Plugin detects 2 incomplete todos
- Injects continuation prompt: "The boulder never stops..."
- Forces continuation until all tasks complete
- Cannot stop without explicit explanation

## Test 5: Adaptive Rules

**Objective:** Verify rules evolve based on friction logs.

**Steps:**
1. Complete several tasks with friction logging
2. Check `.nexus/enforcer-rules.json` for updates
3. Verify local overrides in `.nexus/enforcer-rules.local.json`

**Expected Result:**
- Rules adapt based on patterns
- Local overrides respected
- Base rules remain stable

## Test 6: Agent-Specific Prompts

**Objective:** Verify prompts are formatted correctly for different agents.

**Steps:**
1. Test with OpenCode (already configured)
2. Check prompt format in logs

**Expected Result:**
- OpenCode: Uses [SYSTEM DIRECTIVE: ...] format
- Prompts are clear and actionable
- Agent recognizes and responds to enforcement

## When to Restart OpenCode

**Restart OpenCode when:**

1. ✅ **After installing the plugin for the first time**
   - Close all OpenCode instances
   - Restart: `opencode`
   - Verify plugin loads in new session

2. ✅ **After modifying `.opencode/opencode.json`**
   - Configuration changes require restart
   - Plugin settings (enabled/disabled)
   - Enforcement thresholds

3. ✅ **After rebuilding packages**
   - Run: `task build`
   - Restart: `opencode`
   - New code takes effect

4. ✅ **After updating plugin dependencies**
   - If you update nexus-enforcer core
   - Rebuild and restart

5. ❌ **NOT required for:**
   - Friction log updates
   - Regular file edits
   - Git operations
   - Creating workspaces

## Debugging

If plugin doesn't work:

1. **Check plugin loaded:**
   ```bash
   opencode --version  # Should show no errors
   # Check logs for "nexus-opencode" mentions
   ```

2. **Verify build:**
   ```bash
   ls packages/opencode/dist/  # Should have index.js
   ```

3. **Check config:**
   ```bash
   cat .opencode/opencode.json
   ```

4. **Test in isolation:**
   ```bash
   cd /tmp && opencode  # Should NOT load nexus plugin
   ```

## Success Criteria

All tests pass when:

- [ ] Plugin loads without errors
- [ ] Workspace enforcement blocks non-workspace writes
- [ ] Dogfooding verification requires friction log
- [ ] Task completion blocked with incomplete todos
- [ ] Prompts are injected at correct lifecycle points
- [ ] Agent cannot stop without explanation
- [ ] Rules adapt based on project history

## Next Steps After Testing

Once tests pass:

1. Document any adjustments to prompts
2. Update enforcement thresholds if needed
3. Consider publishing to npm
4. Set up CI/CD for automated testing

## Current Status

- [x] Packages built successfully
- [x] Project-specific config created
- [ ] Plugin loading test: **PENDING RESTART**
- [ ] Workspace enforcement test: PENDING
- [ ] Dogfooding verification test: PENDING
- [ ] Task completion test: PENDING
- [ ] Adaptive rules test: PENDING

**Action Required:** Restart OpenCode to load the new plugin configuration.
