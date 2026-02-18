---
name: nexus-dogfooding
description: Enforces commit quality and continuous dogfooding practices for nexus development
---

# Nexus Dogfooding Skill

## Commit Quality Enforcement

**BEFORE any git commit, you MUST:**

1. **Validate Conventional Commits format:**
   - Types: feat, fix, docs, style, refactor, test, chore, ci, build, perf
   - Format: `<type>(scope): <description>`

2. **Check for binaries:**
   - Never commit .exe, .dll, .so, .dylib, or the `nexus` binary
   - The nexus binary is built by GitHub Actions

3. **Verify dogfooding:**
   - Did I test this change myself?
   - Did I use a nexus workspace?
   - Is friction logged?

4. **Atomic commits:**
   - Max 10 files per commit
   - Max 500 lines changed

## Quality Gates

- Conventional Commits format
- No binaries
- Tests pass
- Dogfooded
- Friction logged

## Reminder

The boulder never stops rolling.
