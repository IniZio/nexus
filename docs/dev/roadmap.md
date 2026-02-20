# Roadmap

## Current Status

**MVP Complete** - All critical features implemented and tested.

## Completed Milestones

### ✅ MVP Features (Complete)

| Feature | Status | Tests |
|---------|--------|-------|
| Container Workspaces | Complete | 26 tests |
| Multi-Service Templates | Complete | 29 tests |
| Task Verification | Complete | 56 tests |
| Ralph Loop | Complete | 22 tests |
| Agent Management | Complete | 20 tests |

### ✅ Statistics

- **Source Code:** ~4,273 lines
- **Test Code:** ~5,598 lines
- **Test Ratio:** 1.3:1
- **Test Functions:** 153

## Upcoming Features

### Phase 1: Git Worktree Integration

**Goal:** Each workspace has an isolated git branch.

**Status:** Implemented

- Auto-create git worktree on `workspace create`
- Mount worktree to container (not project root)
- Branch naming: `nexus/<workspace-name>`
- Sync changes between workspace and main

**Related ADRs:**
- [001 - Worktree Isolation](decisions/001-worktree-isolation.md)

### Phase 2: Essential Multi-Service Templates

**Goal:** One-command full dev environment.

**Status:** Implemented

**Templates Available:**
1. **node-postgres** - React/Vue + Node API + PostgreSQL
2. **python-postgres** - Flask/Django + PostgreSQL
3. **go-postgres** - Go API + PostgreSQL

### Phase 3: Simple Parallel Execution

**Goal:** Run 2-3 agents simultaneously.

**Status:** Implemented (simplified scope)

- Assign independent tasks to multiple agents
- Basic conflict detection
- No complex dependency resolution

### Phase 4: Polish & Documentation

**Goal:** Usable by others.

**Status:** In Progress

- README with quickstart ✓
- Example projects
- Troubleshooting guide
- Performance optimization

## Post-MVP Features

The following are planned for future releases:

### Remote Workspaces
SSH to other Docker hosts for distributed development.

### Web UI
Visual task board and workspace management.

### Advanced Parallel Coordination
5+ agents with complex dependency resolution.

### Additional Templates
- rust-postgres
- java-postgres
- .NET postgresql

### Plugin System
Extend Nexus with custom providers and templates.

## Boulder Enforcement

The Nexus Enforcer is a multi-agent enforcement system that ensures AI agents complete tasks fully, use workspaces, and dogfood their changes.

### Core Features

| Feature | Status | Description |
|---------|--------|-------------|
| Workspace Enforcement | ✅ Active | Blocks writes outside nexus workspaces |
| Dogfooding Checks | ✅ Active | Requires friction logs before completion |
| Boulder Continuation | ✅ Active | Reminds about incomplete todos |
| Self-Evolving Rules | ✅ Active | Loads config from `.nexus/enforcer-config.json` |

### Roadmap

#### Phase 1: Core Enforcement (In Progress)

**Goal:** Make the boulder roll for OpenCode

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

#### Phase 2: Multi-Agent Support

**Goal:** Boulder rolls in Claude, Cursor, etc.

- [ ] Build Claude plugin (uses Claude SDK)
- [ ] Build Cursor extension (VSCode extension format)
- [ ] Build Copilot integration (if possible)
- [ ] Test cross-agent consistency

**Success Criteria:**
- Same enforcement regardless of AI assistant
- Consistent prompt formatting per agent
- Shared core library (nexus-enforcer)

#### Phase 3: Self-Evolving Rules

**Goal:** Enforcement learns from friction

- [ ] Implement consolidation step in nexus CLI
- [ ] Analyze friction logs for patterns
- [ ] Auto-update `.nexus/enforcer-rules.json`
- [ ] Support per-project custom rules
- [ ] Support per-user local overrides

**Success Criteria:**
- Rules adapt based on project history
- Common issues get automatic checks
- Users can customize without breaking base rules

#### Phase 4: Advanced Features

**Goal:** Deep integration and insights

- [ ] Telemetry on enforcement effectiveness
- [ ] Dashboard for team dogfooding metrics
- [ ] Integration with nexus pulse (existing telemetry)
- [ ] MCP server for external tool integration
- [ ] Auto-fix suggestions (not just blocks)

**Success Criteria:**
- Teams can see dogfooding compliance
- Enforcement is measurable and improvable
- Integration with existing nexus workflows

## Related Documentation

- [Architecture](explanation/architecture.md)
- [Architecture Decisions](dev/decisions/)
