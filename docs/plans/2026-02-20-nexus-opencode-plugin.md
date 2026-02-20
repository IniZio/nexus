# Nexus OpenCode Plugin - Implementation Plan

**Date:** 2026-02-20
**Status:** Draft

---

## Executive Summary

Create an installable OpenCode plugin (`@nexus/opencode-plugin`) that generalizes the boulder enforcement system and provides hierarchical task management. The plugin will be distributed via npm and installable in any OpenCode project.

## Key Learnings from nexus-old

**What Failed:**
- Workspace-scoped only (not project-wide)
- Required Go binaries (complex deployment)
- Flat task structure (no hierarchy)
- SQLite per workspace (hard to query across)

**What Succeeded:**
- Clean service/storage abstraction
- SQLite persistence model
- Task statuses and priorities

**New Approach:**
- OpenCode SDK plugin (JavaScript, no binaries)
- JSON file-based state (simple, portable)
- Hierarchical tasks (Epic → Story → Task)
- Multi-boulder parallel enforcement
- npm package distribution

---

## Architecture Overview

### Core Components

```
@nexus/opencode-plugin/
├── src/
│   ├── index.ts              # Plugin entry point
│   ├── types/
│   │   ├── task.ts           # Task type definitions
│   │   ├── epic.ts           # Epic type definitions
│   │   └── boulder.ts        # Boulder state types
│   ├── core/
│   │   ├── task-manager.ts   # Hierarchical task management
│   │   ├── epic-manager.ts   # Epic management
│   │   └── state-manager.ts  # JSON state persistence
│   ├── enforcers/
│   │   ├── base-enforcer.ts  # Base boulder enforcer
│   │   ├── epic-enforcer.ts  # Epic-level enforcement
│   │   ├── story-enforcer.ts # Story-level enforcement
│   │   └── task-enforcer.ts  # Task-level enforcement
│   ├── commands/
│   │   ├── task-create.ts    # /nexus-task-create
│   │   ├── task-list.ts      # /nexus-task-list
│   │   ├── epic-create.ts    # /nexus-epic-create
│   │   ├── status.ts         # /nexus-status
│   │   ├── pause.ts          # /nexus-pause
│   │   └── resume.ts         # /nexus-resume
│   └── storage/
│       └── json-storage.ts   # JSON file storage
├── tests/
│   └── *.test.ts
├── package.json
└── README.md
```

### Data Model

```typescript
// Hierarchy: Epic → Story → Task

interface Epic {
  id: string;
  title: string;
  description: string;
  status: 'active' | 'paused' | 'completed';
  stories: string[]; // Story IDs
  boulderState: BoulderState;
  createdAt: string;
  updatedAt: string;
}

interface Story {
  id: string;
  epicId: string;
  title: string;
  description: string;
  status: 'pending' | 'in_progress' | 'completed';
  tasks: string[]; // Task IDs
  boulderState: BoulderState;
  createdAt: string;
  updatedAt: string;
}

interface Task {
  id: string;
  storyId: string;
  epicId: string;
  title: string;
  description: string;
  status: 'pending' | 'in_progress' | 'completed';
  boulderState: BoulderState;
  createdAt: string;
  updatedAt: string;
}

interface BoulderState {
  iteration: number;
  lastActivity: number;
  lastEnforcement: number;
  status: 'CONTINUOUS' | 'PAUSED' | 'ENFORCING';
  pauseFlagPath: string;
}
```

### State Storage

```
.nexus/
├── nexus-plugin/
│   ├── epics/
│   │   └── {epic-id}.json
│   ├── stories/
│   │   └── {story-id}.json
│   ├── tasks/
│   │   └── {task-id}.json
│   ├── boulders/
│   │   ├── epic-{id}.flag
│   │   ├── story-{id}.flag
│   │   └── task-{id}.flag
│   └── index.json          # Master index of all epics
```

---

## Implementation Tasks

### Phase 1: Package Setup (Tasks 1-3)

**Task 1: Create Package Structure**
- Create `packages/opencode-plugin/` directory
- Initialize `package.json` with proper metadata
- Set up `tsconfig.json` for TypeScript compilation
- Create `.gitignore` for node_modules and dist

**Task 2: Type Definitions**
- Create `src/types/task.ts` with Task interface
- Create `src/types/epic.ts` with Epic and Story interfaces
- Create `src/types/boulder.ts` with BoulderState interface
- Export all types from `src/types/index.ts`

**Task 3: JSON Storage Layer**
- Create `src/storage/json-storage.ts`
- Implement read/write operations for JSON files
- Ensure directory creation (mkdir -p equivalent)
- Handle errors gracefully

### Phase 2: Core Managers (Tasks 4-6)

**Task 4: State Manager**
- Create `src/core/state-manager.ts`
- Implement singleton pattern for state access
- Methods: `loadEpic()`, `saveEpic()`, `loadStory()`, `saveStory()`, `loadTask()`, `saveTask()`
- Maintain master index of all epics

**Task 5: Task Manager**
- Create `src/core/task-manager.ts`
- Implement CRUD operations for tasks
- Handle parent-child relationships (story → task)
- Generate unique IDs (uuid or timestamp-based)

**Task 6: Epic Manager**
- Create `src/core/epic-manager.ts`
- Implement CRUD operations for epics and stories
- Handle epic → story → task hierarchy
- List all epics with status summary

### Phase 3: Boulder Enforcers (Tasks 7-10)

**Task 7: Base Enforcer**
- Create `src/enforcers/base-enforcer.ts`
- Abstract base class with common enforcement logic
- Methods: `checkIdle()`, `checkCooldown()`, `shouldEnforce()`, `enforce()`
- Configurable thresholds (30s idle, 30s cooldown)

**Task 8: Epic Enforcer**
- Create `src/enforcers/epic-enforcer.ts`
- Extends BaseEnforcer
- Monitors epic-level idle time
- Shows toast: "[EPIC] {epic-title} - Iteration {n}"

**Task 9: Story Enforcer**
- Create `src/enforcers/story-enforcer.ts`
- Extends BaseEnforcer
- Monitors story-level idle time
- Shows toast: "[STORY] {epic-title} → {story-title} - Iteration {n}"

**Task 10: Task Enforcer**
- Create `src/enforcers/task-enforcer.ts`
- Extends BaseEnforcer
- Monitors task-level idle time
- Shows toast: "[TASK] {epic-title} → {story-title} → {task-title} - Iteration {n}"

### Phase 4: OpenCode Commands (Tasks 11-16)

**Task 11: Command Infrastructure**
- Create `src/commands/index.ts`
- Set up command registry
- Implement command validation

**Task 12: Task Commands**
- `/nexus-task-create {title} [--epic {id}] [--story {id}]`
- `/nexus-task-list [--status {status}] [--epic {id}]`
- `/nexus-task-complete {id}`
- `/nexus-task-delete {id}`

**Task 13: Epic Commands**
- `/nexus-epic-create {title} [description]`
- `/nexus-epic-list [--status {status}]`
- `/nexus-epic-status {id}`
- `/nexus-story-create {epic-id} {title}`

**Task 14: Status Command**
- `/nexus-status`
- Shows current hierarchy with boulder states
- Displays all active epics, stories, tasks
- Shows iteration counts

**Task 15: Pause/Resume Commands**
- `/nexus-pause [--epic {id}] [--story {id}] [--task {id}]`
- `/nexus-resume [--epic {id}] [--story {id}] [--task {id}]`
- Creates/removes pause flag files

**Task 16: Plugin Entry Point**
- Create `src/index.ts`
- Export default plugin function
- Register all hooks (tool.execute.before, message, event, command.execute)
- Initialize all enforcers
- Start monitoring loops

### Phase 5: Integration & Testing (Tasks 17-20)

**Task 17: OpenCode Integration**
- Create example `opencode.json` configuration
- Document plugin registration
- Test with real OpenCode instance

**Task 18: Unit Tests**
- Test state manager (CRUD operations)
- Test task manager (hierarchy operations)
- Test enforcers (idle detection, enforcement)
- Test commands (input parsing, execution)

**Task 19: Integration Tests**
- Test full workflow: create epic → story → task
- Test boulder enforcement at each level
- Test pause/resume functionality
- Test concurrent operations

**Task 20: Documentation**
- Write comprehensive README.md
- Document all commands with examples
- Document configuration options
- Document troubleshooting guide

### Phase 6: npm Publication (Tasks 21-22)

**Task 21: Build & Package**
- Set up build pipeline (`npm run build`)
- Generate type declarations
- Create LICENSE file (MIT)
- Prepare files for publication

**Task 22: Publish to npm**
- Create npm account if needed
- Configure npm auth
- Publish package: `npm publish --access public`
- Tag release on GitHub

---

## Configuration

### User opencode.json

```json
{
  "$schema": "https://opencode.ai/config.json",
  "plugin": ["@nexus/opencode-plugin"],
  "command": {
    "nexus-task-create": {
      "template": "Create a new task in the Nexus system",
      "description": "Create task"
    },
    "nexus-task-list": {
      "template": "List all tasks",
      "description": "List tasks"
    },
    "nexus-epic-create": {
      "template": "Create a new epic",
      "description": "Create epic"
    },
    "nexus-status": {
      "template": "Show Nexus system status",
      "description": "Show status"
    },
    "nexus-pause": {
      "template": "Pause boulder enforcement",
      "description": "Pause enforcement"
    },
    "nexus-resume": {
      "template": "Resume boulder enforcement",
      "description": "Resume enforcement"
    }
  }
}
```

### Plugin Configuration (optional .nexus/nexus-plugin.json)

```json
{
  "idleThresholdMs": 30000,
  "cooldownMs": 30000,
  "maxEnforcementDepth": 3,
  "autoCreateStories": false,
  "enforceOnEpics": true,
  "enforceOnStories": true,
  "enforceOnTasks": true
}
```

---

## Usage Examples

### Creating Hierarchy

```bash
# Create epic
/nexus-epic-create "Build Authentication System" "Implement OAuth and session management"

# Create story in epic
/nexus-story-create epic-abc-123 "OAuth Integration"

# Create task in story
/nexus-task-create "Setup OAuth provider" --epic epic-abc-123 --story story-xyz-789
```

### Viewing Status

```bash
/nexus-status

# Output:
# 📊 Nexus Status
# 
# 🎯 Epic: Build Authentication System [Iteration: 12]
#   📖 Story: OAuth Integration [Iteration: 5]
#     ✅ Task: Setup OAuth provider [COMPLETED]
#     🔄 Task: Configure callbacks [Iteration: 3]
#   📖 Story: Session Management [Iteration: 2]
#     ⏳ Task: Create session store [PENDING]
```

### Pausing/Resuming

```bash
# Pause specific task
/nexus-pause --task task-def-456

# Pause entire epic
/nexus-pause --epic epic-abc-123

# Resume
/nexus-resume --epic epic-abc-123
```

---

## Success Criteria

- [ ] Plugin installable via `npm install @nexus/opencode-plugin`
- [ ] All 6 commands working in OpenCode
- [ ] Hierarchical task management (Epic → Story → Task)
- [ ] Multi-boulder enforcement at each level
- [ ] Pause/resume functionality
- [ ] JSON-based persistence (no SQLite)
- [ ] Unit tests passing (90%+ coverage)
- [ ] Integration tests passing
- [ ] Documentation complete
- [ ] Published to npm registry

---

## Appendix: File Structure

```
packages/opencode-plugin/
├── src/
│   ├── index.ts
│   ├── types/
│   │   ├── index.ts
│   │   ├── task.ts
│   │   ├── epic.ts
│   │   └── boulder.ts
│   ├── core/
│   │   ├── index.ts
│   │   ├── state-manager.ts
│   │   ├── task-manager.ts
│   │   └── epic-manager.ts
│   ├── enforcers/
│   │   ├── index.ts
│   │   ├── base-enforcer.ts
│   │   ├── epic-enforcer.ts
│   │   ├── story-enforcer.ts
│   │   └── task-enforcer.ts
│   ├── commands/
│   │   ├── index.ts
│   │   ├── task-create.ts
│   │   ├── task-list.ts
│   │   ├── epic-create.ts
│   │   ├── epic-list.ts
│   │   ├── status.ts
│   │   ├── pause.ts
│   │   └── resume.ts
│   └── storage/
│       ├── index.ts
│       └── json-storage.ts
├── tests/
│   ├── state-manager.test.ts
│   ├── task-manager.test.ts
│   ├── epic-manager.test.ts
│   ├── enforcers.test.ts
│   └── commands.test.ts
├── package.json
├── tsconfig.json
├── README.md
└── LICENSE
```

---

**Plan complete! Ready for implementation.**
