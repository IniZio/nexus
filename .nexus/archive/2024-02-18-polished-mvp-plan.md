# Nexus Polished MVP Plan
## From "Works" to "Production-Ready"

**Status:** Planning Phase  
**Goal:** Make Nexus so polished that it replaces built-in agent task tools  
**Approach:** Dogfooding → Telemetry → Documentation → Polish

---

## Phase 1: Dogfooding Probe Design (Week 1)

### Objective
Use Nexus to develop Nexus itself. Every friction point is a bug. Every workaround is a missing feature.

### Probe Categories

#### 1. Workspace Creation Probes
**What to test:**
- Time from command to ready-to-code (< 30s target)
- Template application accuracy
- Port allocation without conflicts
- Git worktree creation correctness
- SSH key setup transparency

**Data to collect:**
```yaml
probe: workspace_creation
metrics:
  - total_duration_ms
  - template_download_time_ms
  - docker_pull_time_ms
  - worktree_creation_time_ms
  - port_allocation_attempts
  - errors_encountered: []
success_criteria:
  - duration < 30000ms
  - port_allocation_attempts == 1
  - errors == 0
```

**Success Criteria:**
- [ ] Workspace ready in < 30 seconds
- [ ] Zero manual intervention required
- [ ] All services accessible on first try
- [ ] Git branch created and checked out automatically

---

#### 2. Daily Development Probes
**What to test:**
- Context switching between workspaces
- File sync between container and host
- Service accessibility (no port confusion)
- Git operations within workspace
- Task creation and assignment flow

**Daily Log Template:**
```yaml
date: 2026-02-18
workspaces_active: 3
context_switches: 12
friction_points:
  - type: "command_confusion"
    description: "Forgot whether to use 'up' or 'start'"
    severity: low
  - type: "port_collision"  
    description: "Port 3000 already in use by other workspace"
    severity: medium
workarounds_used:
  - "Manually edited docker-compose.yml to change port"
  - "Used 'docker ps' to find correct port"
wish_list:
  - "Auto-detect when I switch git branches"
  - "Show workspace status in prompt"
```

---

#### 3. Task Management Probes
**Goal:** Prove Nexus task management is better than built-in agent tools

**Comparison Matrix:**

| Feature | Built-in (Opencode/Claude) | Nexus Target |
|---------|---------------------------|--------------|
| Persistence | Session-only | SQLite + git |
| Verification | None | Mandatory gates |
| Cross-session | No | Yes |
| Team sharing | No | Yes (git-based) |
| Automation | Limited | Ralph loop |
| History | Lost on restart | Permanent |
| Integration | CLI-only | Workspace-scoped |

**Probes:**
1. Create task → Restart session → Task still exists?
2. Assign task → Complete → Verify → Auto-improve skill?
3. Multiple agents → Task dependencies → Parallel execution?
4. Task rejection → History tracked → Patterns detected?

**Success Criteria:**
- [ ] Tasks survive session restart (unlike built-in)
- [ ] Verification catches 100% of incomplete work
- [ ] Rejection feedback improves process
- [ ] No need to re-explain context to new agent

---

#### 4. Multi-Workspace Probes
**What to test:**
- Working on 2+ features simultaneously
- Isolation between workspaces (no file conflicts)
- Resource management (memory, CPU)
- Port allocation across workspaces

**Test Scenario:**
```bash
# Create 3 workspaces
nexus workspace create feature-auth --template node-postgres
nexus workspace create feature-payment --template node-postgres  
nexus workspace create bugfix-login --template node-postgres

# Work on all 3 simultaneously
# - feature-auth: implement JWT
# - feature-payment: add Stripe integration
# - bugfix-login: fix redirect bug

# Verify:
# - Each has isolated git branch
# - Each has isolated database
# - No port conflicts
# - Can switch between them instantly
```

---

#### 5. Error Recovery Probes
**What to test:**
- Container crash recovery
- Docker daemon restart handling
- Git worktree corruption recovery
- Port already in use errors
- Network connectivity issues

**Failure Injection Tests:**
```bash
# Test 1: Kill container mid-work
nexus workspace up feature-auth
docker kill nexus-feature-auth
nexus workspace up feature-auth  # Should recover gracefully

# Test 2: Delete worktree manually
rm -rf .nexus/worktrees/feature-auth/
nexus workspace list  # Should detect inconsistency

# Test 3: Port conflict
# Start something on port 3000
nexus workspace up feature-auth  # Should find alt port
```

---

### Dogfooding Schedule

**Week 1 Structure:**

| Day | Activity | Focus |
|-----|----------|-------|
| **Mon** | Setup | Create initial workspaces, document baseline |
| **Tue** | Feature Dev | Implement telemetry system using Nexus |
| **Wed** | Bug Fix | Fix issues found in Tue |
| **Thu** | Multi-workspace | Work on 3 features simultaneously |
| **Fri** | Documentation | Write docs using Nexus workflow |
| **Weekend** | Passive | Let Ralph analyze patterns |

**Daily Checklist:**
- [ ] Log all friction points
- [ ] Time each operation
- [ ] Note workarounds used
- [ ] Rate satisfaction (1-10)
- [ ] Suggest one improvement

---

## Phase 2: Local Telemetry System (Week 1-2)

### Design Philosophy
**Local-first, user-owned data.** No opt-in needed because data never leaves user's machine unless they explicitly choose to sync.

### Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    User's Machine                          │
│  ┌─────────────┐    ┌─────────────┐    ┌─────────────┐    │
│  │  Nexus CLI  │───▶│  Telemetry  │───▶│  SQLite DB  │    │
│  │  (Events)   │    │  (Collect)  │    │ (analytics) │    │
│  └─────────────┘    └─────────────┘    └─────────────┘    │
│                              │                              │
│                              ▼                              │
│                       ┌─────────────┐                       │
│                       │  Dashboard  │                       │
│                       │  (Query)    │                       │
│                       └─────────────┘                       │
│                              │                              │
│                              ▼                              │
│                       ┌─────────────┐                       │
│                       │  Insights   │                       │
│                       │  (Patterns) │                       │
│                       └─────────────┘                       │
│                              │                              │
└──────────────────────────────┼──────────────────────────────┘
                               │
                    ┌──────────▼──────────┐
                    │   Optional Sync     │
                    │  (User initiates)   │
                    └──────────┬──────────┘
                               │
                    ┌──────────▼──────────┐
                    │  External Service   │
                    │  (User's choice)    │
                    └─────────────────────┘
```

### Data Model

**Events Table:**
```sql
CREATE TABLE events (
    id INTEGER PRIMARY KEY,
    timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
    session_id TEXT NOT NULL,
    event_type TEXT NOT NULL,  -- 'command', 'workspace', 'task', 'error'
    
    -- Anonymized identifiers (hashed)
    workspace_hash TEXT,       -- SHA256 of workspace name
    task_hash TEXT,            -- SHA256 of task ID
    
    -- Event details
    command TEXT,              -- 'workspace create'
    duration_ms INTEGER,       -- How long it took
    success BOOLEAN,
    error_category TEXT,       -- 'port_conflict', 'timeout', etc.
    
    -- Context (non-sensitive)
    template_used TEXT,        -- 'node-postgres'
    services_count INTEGER,    -- Number of services
    ports_used INTEGER,        -- Number of ports mapped
    
    -- Raw data for debugging (optional, user-controlled)
    raw_data JSON              -- Full event details
);
```

**Sessions Table:**
```sql
CREATE TABLE sessions (
    id INTEGER PRIMARY KEY,
    session_id TEXT UNIQUE NOT NULL,
    started_at DATETIME,
    ended_at DATETIME,
    duration_ms INTEGER,
    commands_executed INTEGER,
    workspaces_created INTEGER,
    tasks_completed INTEGER,
    errors_encountered INTEGER,
    user_feedback TEXT         -- Optional free-form
);
```

**Patterns Table:**
```sql
CREATE TABLE patterns (
    id INTEGER PRIMARY KEY,
    pattern_type TEXT NOT NULL,  -- 'slow_command', 'recurring_error'
    description TEXT,
    frequency INTEGER,
    first_seen DATETIME,
    last_seen DATETIME,
    affected_sessions TEXT,      -- JSON array of session IDs
    suggested_fix TEXT
);
```

### CLI Commands

```bash
# Telemetry control (no opt-in needed - local only)
nexus telemetry status          # Show what's being collected
nexus telemetry on              # Enable collection
nexus telemetry off             # Disable collection
nexus telemetry purge           # Delete all data

# Local analytics
nexus stats                     # Show usage stats
nexus stats --week             # Last 7 days
nexus stats --month            # Last 30 days

# Insights
nexus insights                  # Show patterns detected
nexus insights --slow          # Slow operations
nexus insights --errors        # Common errors
nexus insights --improvements  # Suggested improvements

# Export (user owns their data)
nexus export --format json     # Export to JSON
nexus export --format csv      # Export to CSV
nexus export --sql             # Raw SQLite dump

# Optional external sync (explicit user action)
nexus sync setup               # Configure sync endpoint
nexus sync now                 # One-time sync
nexus sync auto on             # Auto-sync (opt-in)
```

### Privacy-First Features

1. **No Cloud by Default**: Everything stays on user's machine
2. **Explicit Sync**: User must run `nexus sync` to send data anywhere
3. **Anonymized IDs**: Workspace/task names hashed
4. **No Code Content**: Never collect source code or file contents
5. **User Controls**: Can purge, export, or disable anytime
6. **Open Format**: SQLite database - user can inspect with any tool

### Dashboard (Local)

```bash
$ nexus dashboard

╔══════════════════════════════════════════════════════════╗
║  Nexus Usage Analytics (Last 30 Days)                   ║
╠══════════════════════════════════════════════════════════╣
║                                                          ║
║  Workspaces Created:    12                               ║
║  Tasks Completed:       47                               ║
║  Avg Session Length:    2h 15m                           ║
║  Success Rate:          94%                              ║
║                                                          ║
║  Top Templates:                                          ║
║    1. node-postgres     (45%)                           ║
║    2. go-postgres       (30%)                           ║
║    3. python-postgres   (25%)                           ║
║                                                          ║
║  Common Issues:                                          ║
║    ⚠️  Port 3000 conflicts (5 times)                    ║
║    ⚠️  Slow 'workspace up' (>30s, 3 times)              ║
║                                                          ║
║  Insights:                                               ║
║    💡 Consider using port 3001 for web services         ║
║    💡 'workspace create' is 2x faster with cached image ║
║                                                          ║
╚══════════════════════════════════════════════════════════╝
```

---

## Phase 3: Documentation with Zensical (Week 2)

### Why Zensical?
- Beautiful Material Design docs
- Built-in search
- Mobile-responsive
- Easy GitHub Pages deployment
- Markdown-native

### Setup

**1. Install Zensical:**
```bash
pip install zensical
```

**2. Configuration (`zensical.yml`):**
```yaml
site_name: Nexus Documentation
site_url: https://inizio.github.io/nexus
site_author: IniZio

repo_name: inizio/nexus
repo_url: https://github.com/inizio/nexus

nav:
  - Home: index.md
  - Getting Started:
    - Installation: getting-started/installation.md
    - Quick Start: getting-started/quickstart.md
    - Your First Workspace: getting-started/first-workspace.md
  - Core Concepts:
    - Workspaces: concepts/workspaces.md
    - Tasks: concepts/tasks.md
    - Agents: concepts/agents.md
    - Verification: concepts/verification.md
  - User Guide:
    - CLI Reference: user-guide/cli.md
    - Templates: user-guide/templates.md
    - Best Practices: user-guide/best-practices.md
    - Troubleshooting: user-guide/troubleshooting.md
  - Development:
    - Architecture: dev/architecture.md
    - Contributing: dev/contributing.md
    - Dogfooding: dev/dogfooding.md
  - Roadmap: roadmap.md
  - Changelog: changelog.md

theme:
  name: material
  palette:
    - scheme: default
      primary: indigo
      accent: indigo
  features:
    - navigation.tabs
    - navigation.sections
    - navigation.expand
    - search.suggest
    - search.highlight

plugins:
  - search
  - minify
```

**3. GitHub Actions Workflow (`.github/workflows/docs.yml`):**
```yaml
name: Documentation
on:
  push:
    branches:
      - main
    paths:
      - 'docs/**'
      - 'zensical.yml'

permissions:
  contents: read
  pages: write
  id-token: write

jobs:
  deploy:
    environment:
      name: github-pages
      url: ${{ steps.deployment.outputs.page_url }}
    runs-on: ubuntu-latest
    steps:
      - uses: actions/configure-pages@v5
      - uses: actions/checkout@v5
      - uses: actions/setup-python@v5
        with:
          python-version: 3.x
      - run: pip install zensical
      - run: zensical build --clean
      - uses: actions/upload-pages-artifact@v4
        with:
          path: site
      - uses: actions/deploy-pages@v4
        id: deployment
```

**4. Document Structure:**

```
docs/
├── index.md                    # Landing page
├── getting-started/
│   ├── installation.md
│   ├── quickstart.md
│   └── first-workspace.md
├── concepts/
│   ├── workspaces.md
│   ├── tasks.md
│   ├── agents.md
│   └── verification.md
├── user-guide/
│   ├── cli.md
│   ├── templates.md
│   ├── best-practices.md
│   └── troubleshooting.md
├── dev/
│   ├── architecture.md
│   ├── contributing.md
│   └── dogfooding.md
├── roadmap.md
└── changelog.md
```

---

## Phase 4: Ralph Core Documentation (Future Roadmap)

### Current Implementation
Simple Ralph loop with feedback collection and skill updates.

### Future Vision (Documented, Not Implemented)

#### Multi-Agent Orchestration
```yaml
# Ralph Orchestration Architecture (Future)
agents:
  architect:
    role: Design and planning
    triggers:
      - new_feature_request
      - complex_task_created
    outputs:
      - design_doc
      - implementation_plan
      
  executor:
    role: Implementation
    triggers:
      - plan_approved
    outputs:
      - code_changes
      - tests
      
  reviewer:
    role: Code review
    triggers:
      - implementation_complete
    outputs:
      - review_feedback
      - approval/rejection
      
  researcher:
    role: Investigation
    triggers:
      - unknown_technology
      - complex_bug
    outputs:
      - findings
      - recommendations

workflow_phases:
  1_planning:
    - architect analyzes requirements
    - creates design document
    - breaks down into tasks
    
  2_kickoff:
    - assigns tasks to agents
    - sets dependencies
    - starts parallel work where possible
    
  3_implementation:
    - executors work on tasks
    - researchers investigate blockers
    - regular progress updates
    
  4_verification:
    - reviewers check work
    - automated tests run
    - criteria validation
    
  5_consolidation:
    - merge approved changes
    - update documentation
    - collect feedback
```

#### Context Handoff System
```yaml
# Context Estimation (Future)
context_management:
  estimation:
    method: heuristic_token_count
    threshold: 0.78  # 78% of context limit
    
  triggers:
    - large_codebase_changes
    - many_files_modified
    - long_conversation_history
    
  handoff_process:
    1. detect_threshold: Monitor token usage
    2. summarize_context: Compress current state
    3. create_handoff_doc: Write context summary
    4. start_new_session: Fresh context
    5. restore_state: Load from handoff doc
    
  preservation:
    - workspace_state
    - task_status
    - agent_assignments
    - verification_criteria
```

#### Implementation Phases

**Phase A: Basic Multi-Agent (Week 4-5)**
- 3 agent types: executor, reviewer, researcher
- Simple task routing
- No parallel execution yet
- Tests: 15 integration tests

**Phase B: Parallel Execution (Week 6)**
- Independent tasks in parallel
- Basic conflict detection
- Resource management
- Tests: 10 integration tests

**Phase C: Full Orchestration (Week 8+)**
- All 5 agent types
- Complex dependency resolution
- Context handoff
- Tests: 25 integration tests

---

## Phase 5: Lean Codebase Strategy

### Goal
Prove Nexus is better than built-in agent tools by being so smooth you ban the alternatives.

### Anti-Patterns to Avoid

❌ **Don't do this:**
```go
// Over-engineered with interfaces for everything
type WorkspaceProvider interface {
    Create(ctx context.Context, config Config) (*Workspace, error)
    Start(ctx context.Context, id string) error
    Stop(ctx context.Context, id string) error
    // ... 20 more methods
}

type DockerProvider struct { /* 500 lines */ }
type LXCProvider struct { /* 500 lines */ }
type QEMUProvider struct { /* 500 lines */ }
```

✅ **Do this instead:**
```go
// Simple, focused, works
type Provider struct {
    client *docker.Client
}

func (p *Provider) Create(name, worktreePath string) error {
    // Just Docker, just works
}
```

### Code Quality Rules

1. **Single Responsibility**: Each function does one thing
2. **No Premature Abstraction**: No interfaces until 3+ implementations needed
3. **Fail Fast**: Return errors immediately, don't nest deeply
4. **Self-Documenting**: Clear names > comments
5. **Test Coverage**: Every public function has test
6. **Delete Code**: Remove features before adding new ones

### File Size Limits

- **max 300 lines** per file (split if larger)
- **max 50 lines** per function (refactor if larger)
- **max 5 parameters** per function (use struct if more)

### Dependency Minimalism

**Current Dependencies:**
```
docker/docker
spf13/cobra
mattn/go-sqlite3
stretchr/testify (test only)
ory/dockertest (test only)
```

**Future Dependencies (need justification):**
- Each new dependency must solve a real problem
- Prefer standard library
- Vendor if external dependency critical

---

## Success Metrics

### Technical
- [ ] Dogfooding: 0 critical bugs found
- [ ] Telemetry: < 5% operation failure rate
- [ ] Performance: All commands < 5s
- [ ] Tests: > 80% coverage
- [ ] Code: < 5000 lines total

### User Experience
- [ ] Zero workarounds needed during dogfooding
- [ ] Context switching < 10 seconds
- [ ] Task creation to assignment < 30 seconds
- [ ] Workspace creation to coding < 60 seconds

### Adoption (Internal)
- [ ] Use Nexus for all nexus development
- [ ] Zero use of built-in agent task tools
- [ ] 3+ workspaces active simultaneously
- [ ] 10+ tasks completed with verification

---

## Timeline

| Week | Focus | Key Deliverables |
|------|-------|------------------|
| **Week 1** | Dogfooding + Telemetry | Dogfooding report, Telemetry system |
| **Week 2** | Docs + Polish | Zensical docs, Bug fixes |
| **Week 3** | Ralph Docs + Testing | Ralph roadmap, Integration tests |
| **Week 4+** | Multi-Agent (Future) | Basic orchestration |

---

## Immediate Next Steps

1. **Start Dogfooding Today**
   ```bash
   cd /home/newman/magic/nexus-dev/nexus
   nexus init
   nexus workspace create dogfooding-telemetry --template go-postgres
   # Begin implementing telemetry in this workspace
   ```

2. **Create Friction Log**
   ```bash
   mkdir -p .nexus/dogfooding
   touch .nexus/dogfooding/friction-log.md
   # Log every issue immediately
   ```

3. **Set Up Telemetry**
   - Create `pkg/telemetry/` package
   - Start collecting basic events
   - Build local dashboard

4. **Document Ralph Future**
   - Create `docs/dev/ralph-roadmap.md`
   - Design multi-agent architecture
   - Define implementation phases

---

## Questions Answered

**Q: Why no opt-in for telemetry?**  
A: Data stays local. User chooses to sync externally. No privacy concern.

**Q: Why focus on polish over features?**  
A: A polished MVP beats a buggy v1.0. Dogfooding reveals real issues.

**Q: Why document Ralph but not implement?**  
A: Shows vision without complexity. Implement when current system is solid.

**Q: Why keep codebase lean?**  
A: Easier to maintain, faster to iterate, proves point about simplicity.

---

**Ready to execute?** Confirm and I'll start with Phase 1 (Dogfooding probe implementation).
