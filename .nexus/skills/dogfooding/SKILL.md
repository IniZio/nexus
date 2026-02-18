# nexus-dogfooding-skill
# System reminder for continuous dogfooding with quality enforcement
# This is loaded automatically when in a nexus project

## Commit Quality Enforcement

**BEFORE any git commit, you MUST:**

1. **Validate Conventional Commits format:**
   ```
   <type>[optional scope]: <description>
   
   [optional body]
   
   [optional footer(s)]
   ```
   
   Valid types: `feat`, `fix`, `docs`, `style`, `refactor`, `test`, `chore`, `ci`, `build`, `perf`
   
   ✅ GOOD: `feat(workspace): add containerized environment support`
   ✅ GOOD: `fix(telemetry): prevent race condition in stats collection`
   ❌ BAD: `added stuff`
   ❌ BAD: `WIP`
   ❌ BAD: `fix`

2. **Check for binaries:**
   ```bash
   git diff --cached --name-only | grep -E '\.(exe|dll|so|dylib|bin)$'
   ```
   
   **NEVER commit binaries.** They bloat the repo.
   
   The `nexus` binary should be built by GitHub Actions, not committed.

3. **Verify dogfooding:**
   - Have I tested this change myself?
   - Did I use a nexus workspace for this work?
   - Is there friction to log?

4. **Check commit size:**
   - Max 10 files per commit (atomic changes)
   - Max 500 lines changed (reviewable)
   - If larger, split into multiple commits

## Auto-Enforcement Hook

Create `.nexus/git-hooks/commit-msg`:

```bash
#!/bin/bash
# Enforce Conventional Commits format

commit_msg_file=$1
commit_msg=$(cat "$commit_msg_file")

# Check format
if ! echo "$commit_msg" | grep -qE '^(feat|fix|docs|style|refactor|test|chore|ci|build|perf|revert)(\([^)]+\))?!?: .+'; then
    echo "❌ Invalid commit message format!"
    echo ""
    echo "Expected: <type>(<scope>): <description>"
    echo "Types: feat, fix, docs, style, refactor, test, chore, ci, build, perf, revert"
    echo ""
    echo "Examples:"
    echo "  feat(workspace): add support for custom templates"
    echo "  fix(telemetry): handle null duration in stats"
    echo "  docs: update installation instructions"
    exit 1
fi

# Check for binaries
if git diff --cached --name-only | grep -qE '\.(exe|dll|so|dylib|bin)$'; then
    echo "❌ Binary files detected!"
    echo "Never commit binaries. Use GitHub Actions for builds."
    exit 1
fi

echo "✅ Commit message valid"
exit 0
```

## Continuous Dogfooding Checklist

**Every iteration, ask:**

- [ ] Did I work in a nexus workspace?
- [ ] Did I log any friction points?
- [ ] Are tests passing?
- [ ] Is the commit message proper format?
- [ ] No binaries committed?

## Quality Gates

**For every commit:**

```
┌─────────────────────────────────────────┐
│  1. Conventional Commits format         │
│  2. No binaries                         │
│  3. Tests pass                          │
│  4. Dogfooded                           │
│  5. Friction logged                     │
└─────────────────────────────────────────┘
           ↓
     git commit
           ↓
  GitHub Actions builds binary
           ↓
  GitHub Release distributes
```

## Reminder on Every Commit

<remember priority>
**COMMIT QUALITY CHECK:**
- Type correct? (feat/fix/docs/...)
- Scope appropriate?
- Description clear?
- No binaries?
- Dogfooded?

**If no → Fix before committing**
</remember>
