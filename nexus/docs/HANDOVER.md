# Nexus Project Handover Document

**Date:** 2026-02-18  
**From:** Initial Setup Agent  
**To:** OpenCode Instance (You)  
**Status:** ✅ Plugin Loaded, Ready for Development

---

## What Was Built

### 1. Nexus Enforcer Monorepo

**Location:** `/home/newman/magic/nexus-dev/nexus/`

A multi-agent enforcement system that keeps "the boulder rolling" - ensuring AI agents complete tasks fully, use workspaces, and dogfood their changes.

**Structure:**
```
nexus/
├── cmd/nexus/              # Go CLI (main worktree isolation tool)
├── internal/               # Go internal packages
├── pkg/                    # Go public packages
├── packages/               # TypeScript packages (monorepo)
│   ├── enforcer/          # Core enforcement library
│   ├── opencode/          # OpenCode plugin (built → .opencode/plugins/)
│   ├── claude/            # Claude plugin
│   └── cursor/            # Cursor extension
├── .opencode/
│   └── plugins/
│       └── nexus-enforcer.js   # ⭐ ACTIVE PLUGIN
├── opencode.json          # OpenCode config (minimal)
└── .nexus/
    ├── enforcer-config.json      # Base enforcement rules
    └── enforcer-config.local.json # Local overrides (gitignored)
```

### 2. OpenCode Plugin (ACTIVE)

**Status:** ✅ Loaded and running

**Features:**
- ✅ **Workspace Enforcement**: Blocks writes outside nexus workspaces
- ✅ **Dogfooding Checks**: Requires friction logs before completion
- ✅ **Boulder Continuation**: Reminds about incomplete todos
- ✅ **Self-Evolving Rules**: Loads config from `.nexus/enforcer-config.json`

**How It Works:**
1. Hooks into `tool.execute.before` → Checks workspace
2. Hooks into `tool.execute.after` → Checks dogfooding on completion
3. Hooks into `todo.updated` → Reminds about incomplete tasks
4. Uses OpenCode's `$` shell for file operations

### 3. Multi-Agent Skills

**Location:** Multiple `.*/skills/nexus-dogfooding/` directories

Created skills for 9 AI platforms:
- ✅ OpenCode (`.opencode/skills/`)
- ✅ Claude (`.claude/skills/`)
- ✅ Cursor (`.cursor/skills/`)
- ✅ Codex (`.codex/skills/`)
- ✅ Continue (`.continue/skills/`)
- ✅ Windsurf (`.windsurf/skills/`)
- ✅ Roo (`.roo/skills/`)
- ✅ Kiro (`.kiro/skills/`)
- ✅ Copilot (`.github/prompts/`)

### 4. Git History Cleanup

**Completed:**
- ✅ Removed nexus binary from entire git history
- ✅ Fixed commit messages to Conventional Commits format
- ✅ Created GitHub Actions workflow for building releases
- ✅ Created v0.2.0 release with 4 platform binaries

---

## Current State

### What's Working

| Component | Status | Location |
|-----------|--------|----------|
| Go CLI | ✅ Stable | `cmd/nexus/` |
| OpenCode Plugin | ✅ **ACTIVE** | `.opencode/plugins/nexus-enforcer.js` |
| Multi-agent Skills | ✅ Distributed | `.*/skills/` |
| GitHub Actions | ✅ Building | `.github/workflows/` |
| Release Binaries | ✅ Published | GitHub Releases v0.2.0 |

### Current Configuration

**`.nexus/enforcer-config.json`** (committed):
```json
{
  "enabled": true,
  "enforceWorkspace": true,
  "enforceDogfooding": true,
  "blockStopWithTodos": true,
  "adaptive": true,
  "idlePromptThresholdMs": 30000
}
```

**`opencode.json`** (project root):
```json
{
  "$schema": "https://opencode.ai/config.json"
}
```

The plugin auto-loads from `.opencode/plugins/` and reads its config from `.nexus/`.

---

## Immediate Next Steps

### 1. Test the Plugin (DO THIS NOW)

**Test 1: Workspace Enforcement**
```bash
# You're currently in main worktree (not a workspace)
# Try creating a file:
# "Create a test file called test.txt"
# → Should see enforcement error about workspace
```

**Test 2: Create Workspace**
```bash
# Create and enter a workspace:
nexus workspace create test-feature
cd .nexus/worktrees/test-feature
# Restart OpenCode here
# → Now writes should be allowed
```

**Test 3: Dogfooding**
```bash
# In workspace, complete a small task
# Try to say "I'm done" without friction log
# → Should remind about .nexus/dogfooding/friction-log.md
```

### 2. Fix Package Dependencies

The packages in `packages/` reference each other with `workspace:*` but the OpenCode plugin is a standalone bundled file. You need to:

1. Update `packages/opencode/` to properly import from `packages/enforcer/`
2. Or decide if we keep the bundled version in `.opencode/plugins/`
3. Currently: Bundled version works, source packages need work

### 3. Build and Test Other Adapters

| Adapter | Status | Action |
|---------|--------|--------|
| OpenCode | ✅ Working | Test & refine |
| Claude | 📝 Source only | Build plugin |
| Cursor | 📝 Source only | Build extension |

---

## Roadmap

### Phase 1: Core Enforcement (CURRENT - In Progress)

**Goal:** Make the boulder roll for OpenCode

**Tasks:**
- [x] Create plugin structure
- [x] Implement workspace enforcement
- [x] Implement dogfooding checks
- [x] Implement todo continuation
- [ ] Test all enforcement scenarios
- [ ] Refine prompts based on usage
- [ ] Add statistics tracking

**Success Criteria:**
- Agent cannot write files outside workspace
- Agent cannot claim completion without friction log
- Agent cannot stop with incomplete todos

### Phase 2: Multi-Agent Support

**Goal:** Boulder rolls in Claude, Cursor, etc.

**Tasks:**
- [ ] Build Claude plugin (uses Claude SDK)
- [ ] Build Cursor extension (VSCode extension format)
- [ ] Build Copilot integration (if possible)
- [ ] Test cross-agent consistency

**Success Criteria:**
- Same enforcement regardless of AI assistant
- Consistent prompt formatting per agent
- Shared core library (nexus-enforcer)

### Phase 3: Self-Evolving Rules

**Goal:** Enforcement learns from friction

**Tasks:**
- [ ] Implement consolidation step in nexus CLI
- [ ] Analyze friction logs for patterns
- [ ] Auto-update `.nexus/enforcer-rules.json`
- [ ] Support per-project custom rules
- [ ] Support per-user local overrides

**Success Criteria:**
- Rules adapt based on project history
- Common issues get automatic checks
- Users can customize without breaking base rules

### Phase 4: Advanced Features

**Goal:** Deep integration and insights

**Tasks:**
- [ ] Telemetry on enforcement effectiveness
- [ ] Dashboard for team dogfooding metrics
- [ ] Integration with nexus pulse (existing telemetry)
- [ ] MCP server for external tool integration
- [ ] Auto-fix suggestions (not just blocks)

**Success Criteria:**
- Teams can see dogfooding compliance
- Enforcement is measurable and improvable
- Integration with existing nexus workflows

---

## Development Workflow

### Working on the Plugin

```bash
# 1. Edit source (if modifying packages/)
cd /home/newman/magic/nexus-dev/nexus/packages/opencode
# Edit src/index.ts

# 2. Rebuild
cd /home/newman/magic/nexus-dev/nexus
task build

# 3. Copy to plugins directory
cp packages/opencode/dist/index.js .opencode/plugins/nexus-enforcer.js

# 4. Restart OpenCode
# Exit and restart in nexus directory
```

### Testing Changes

```bash
# Quick test in main worktree (should block)
cd /home/newman/magic/nexus-dev/nexus
# Try: "Create a file test.txt" → Should fail

# Test in workspace (should allow)
nexus workspace create test
cd .nexus/worktrees/test
# Try: "Create a file test.txt" → Should work
```

### Committing Changes

```bash
# All commits must follow Conventional Commits:
# feat: New feature
# fix: Bug fix
# docs: Documentation
# refactor: Code refactoring
# test: Tests
# chore: Maintenance

# Example:
git commit -m "feat(opencode): Add friction log reminder"
```

---

## Key Files to Know

| File | Purpose | When to Edit |
|------|---------|--------------|
| `.opencode/plugins/nexus-enforcer.js` | **ACTIVE PLUGIN** | Immediate fixes |
| `packages/opencode/src/` | Plugin source | Feature development |
| `packages/enforcer/src/` | Core library | Shared logic |
| `.nexus/enforcer-config.json` | Base rules | Project defaults |
| `.nexus/enforcer-config.local.json` | Local overrides | Personal preferences |
| `opencode.json` | OpenCode config | Rarely (plugin list) |
| `Taskfile.yml` | Build tasks | New build steps |

---

## Common Issues & Solutions

### Plugin Not Loading

**Symptoms:** `/status` doesn't show nexus-enforcer

**Solutions:**
1. Check file is at `.opencode/plugins/nexus-enforcer.js`
2. Verify ES module format (`export default` not `module.exports`)
3. Restart OpenCode
4. Check OpenCode console for syntax errors

### Enforcement Not Working

**Symptoms:** Can write files without workspace

**Solutions:**
1. Check `.nexus/enforcer-config.json` has `"enabled": true`
2. Verify you're not in an allowed path (some paths are whitelisted)
3. Check OpenCode console for error messages

### Build Failures

**Symptoms:** `task build` fails

**Solutions:**
1. Run `pnpm install` in each package directory
2. Check TypeScript errors: `pnpm run build` in package dir
3. Verify workspace dependencies use `workspace:*` not versions

---

## Your Mission

As the OpenCode instance continuing this work:

1. **Immediate (Today):**
   - Test the plugin thoroughly
   - Fix any bugs found
   - Document behavior in friction log

2. **Short Term (This Week):**
   - Complete Phase 1 (core enforcement)
   - Refine prompts based on testing
   - Build Claude plugin adapter

3. **Medium Term (This Month):**
   - Complete Phase 2 (multi-agent)
   - Implement self-evolving rules (Phase 3)
   - Write comprehensive documentation

4. **Long Term (Ongoing):**
   - Monitor enforcement effectiveness
   - Gather user feedback
   - Iterate on prompts and rules
   - Build community around the boulder

---

## The Boulder Never Stops

**Remember:** This isn't just a plugin - it's a philosophy.

- Tasks must be completed fully, not "mostly done"
- Work must be dogfooded, not just shipped
- The agent must experience the friction it creates
- Users deserve complete, verified, tested work

**When in doubt:** Keep the boulder rolling.

---

## Resources

- **OpenCode Plugin Docs:** https://opencode.ai/docs/plugins/
- **Config Docs:** https://opencode.ai/docs/config/
- **Nexus Repo:** https://github.com/IniZio/nexus
- **Testing Plan:** `docs/testing/ENFORCER_TESTING.md`

---

## Questions?

If something is unclear:
1. Check the testing plan: `docs/testing/ENFORCER_TESTING.md`
2. Look at plugin source: `.opencode/plugins/nexus-enforcer.js`
3. Review core library: `packages/enforcer/src/`
4. Check friction log: `.nexus/dogfooding/friction-log.md`

**Now go make the boulder roll!** 🐕🪨
