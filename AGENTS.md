# Agent Guidelines

This file provides guidelines for AI agents working in this repository.

## Core Principles

### 1. The Boulder Never Stops
Tasks must be completed fully. A task is NOT complete until:
- All requirements explicitly addressed
- Code works (builds, runs, tests pass)
- Evidence provided (not just "claimed")

### 2. Workspace Isolation
- Use isolated workspaces for feature development
- Never work directly in the main worktree for features
- Create workspaces: `nexus workspace create <name>`

### 3. Dogfooding
- Test your changes in real environments
- Log friction points to `.nexus/dogfooding/friction-log.md`
- Experience the pain you built

### 4. Verification Before Claims
Never claim completion without:
- Verification command output showing success
- Evidence (logs, screenshots, commit hashes)
- Friction log entry if issues encountered

## Quality Gates

Before declaring a task done:
- [ ] Tests pass
- [ ] Build succeeds
- [ ] No type errors
- [ ] No lint errors
- [ ] Dogfooded in workspace
- [ ] Friction logged

## Multi-Agent Support

This file works with:
- Claude (all variants)
- Cursor
- Copilot
- Other AI assistants

## Stopping

If you must stop early, you MUST:
1. List what remains undone
2. Explain why you cannot complete it
3. Specify what the user needs to do next
