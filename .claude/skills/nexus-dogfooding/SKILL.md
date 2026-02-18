---
name: nexus-dogfooding
description: Enforces commit quality and continuous dogfooding practices
---

# Nexus Dogfooding Skill

System reminder for continuous dogfooding with quality enforcement.

## Commit Quality Enforcement

**BEFORE any git commit:**

1. **Validate Conventional Commits format:**
   - Types: feat, fix, docs, style, refactor, test, chore, ci, build, perf, revert
   - Format: `<type>[(scope)]: <description>`
   - ✅ Good: `feat(workspace): add container support`
   - ❌ Bad: `added stuff`, `WIP`, `fix`

2. **Check for binaries:**
   - Never commit .exe, .dll, .so, .dylib, or the `nexus` binary
   - The nexus binary is built by GitHub Actions, distributed via releases

3. **Verify dogfooding:**
   - Did I test this change myself?
   - Did I use a nexus workspace?
   - Is friction logged to `.nexus/dogfooding/friction-log.md`?

4. **Keep commits atomic:**
   - Max 10 files per commit
   - Max 500 lines changed

## Quality Gates

```
✓ Conventional Commits format
✓ No binaries staged
✓ Dogfooded the change
✓ Friction logged
✓ Tests pass
     ↓
  git commit
     ↓
GitHub Actions builds binary
     ↓
GitHub Release distributes
```

## Reminder

**The boulder never stops rolling.**
