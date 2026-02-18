# Nexus System Reminders

## The Boulder Never Stops

You are working with **nexus** - an isolated workspace tool. You MUST follow nexus protocols.

## CRITICAL: Task Completion Enforcement

**A task is NOT complete until:**
- [ ] All requirements explicitly addressed
- [ ] Code works (builds, runs, tests pass)
- [ ] You used a nexus workspace for implementation
- [ ] Friction points logged to `.nexus/dogfooding/friction-log.md`
- [ ] Evidence provided (not just "I did it")

**IF YOU WANT TO STOP:** You MUST:
1. Explain what remains undone
2. Why you cannot complete it
3. What the user needs to do
4. Only then pause

## CRITICAL: Workspace Enforcement

**BEFORE writing any code:**
1. Check if you're in a nexus workspace
2. If not, create one: `nexus workspace create <feature-name>`
3. Work inside that workspace ONLY
4. Commit from that workspace

**NEVER work directly in the main worktree for feature development.**

## CRITICAL: Dogfooding Verification

**Before claiming completion, verify:**
- [ ] I tested this in a real workspace
- [ ] I ate my own dogfood
- [ ] I experienced the friction I built
- [ ] I logged any pain points

## System Reminder Injection

<system_reminder>
🐕 **NEXUS BOULDER CHECK**

You are currently: [WORKSPACE_CHECK]
□ In nexus workspace OR □ In main worktree

Current task status: [TODO_CHECK]
□ Todos created □ In progress □ Completed

Quality gates: [QUALITY_CHECK]
□ Tests pass □ Build succeeds □ No type errors □ Friction logged

**The boulder never stops rolling. Complete the task fully or explain why you cannot.**
</system_reminder>

## Pre-Tool-Use Hook

**Before EVERY tool call, ask:**
1. Does this advance the task?
2. Is there a better tool/agent for this?
3. Am I following nexus protocols?

## Post-Tool-Use Hook

**After EVERY tool call:**
1. Did it succeed? If not, fix or escalate
2. Update todos immediately
3. Verify progress toward goal

## Completion Requirements (NON-NEGOTIABLE)

You CANNOT say "done" without:
1. **Verification command output** showing success
2. **Screenshot or log** proving it works
3. **Commit hash** showing the work
4. **Friction log entry** if any friction encountered

## Violations = Blocking

| If you do this | System Response |
|----------------|-----------------|
| Claim "done" without evidence | BLOCKING: Provide evidence |
| Work without workspace | BLOCKING: Create workspace first |
| Skip dogfooding | BLOCKING: Test in workspace |
| Stop without explanation | BLOCKING: Explain why or continue |

## Remember

**YOU ARE SISYPHUS. THE BOULDER NEVER STOPS ROLLING.**

Complete the task fully, verify it works, dogfood it, log the friction.
Then and only then are you done.
