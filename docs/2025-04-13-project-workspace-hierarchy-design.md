# Project/Workspace Hierarchy Design

> **Goal:** Introduce an explicit "Project" layer above Workspaces to group related workspaces by repository, making the UX closer to Conductor's model.

---

## Background

Currently, Nexus creates workspaces directly from repositories. Users run `nexus create` and get a workspace, but there's no higher-level organization. We want to introduce a **Project** concept that:

1. Implicitly groups workspaces by repository
2. Supports future multi-repo projects
3. Provides clearer visual hierarchy in listings
4. Maintains simple UX (no explicit "create project" step)

---

## Requirements

1. **Implicit project creation**: Projects are auto-created when first workspace for a repo is created
2. **Auto-detect current project**: When in a git repo, treat it as the project context
3. **Hierarchical listing**: `nexus list` shows projects with their workspaces grouped
4. **Multi-repo ready**: Design must accommodate future monorepo/multi-repo features
5. **Keep it simple**: No GitHub PR/Linear integration, minimal new commands

---

## Architecture

### Data Model

```
┌─────────────┐         ┌─────────────┐         ┌─────────────┐
│   Project   │◄───────│  Workspace  │◄───────│   Runtime   │
│  (grouping) │  1:N    │ (isolated   │  1:1    │  (sandbox)  │
│             │         │  env/branch)│         │             │
└─────────────┘         └─────────────┘         └─────────────┘
```

### New Types

**Project (Go):**
```go
type Project struct {
    ID          string    `json:"id"`           // proj-<timestamp>
    Name        string    `json:"name"`         // derived from repo name
    PrimaryRepo string    `json:"primaryRepo"`  // main repo URL/path
    RepoIDs     []string  `json:"repoIds"`      // for future multi-repo
    RootPath    string    `json:"rootPath"`     // storage path
    CreatedAt   time.Time `json:"createdAt"`
    UpdatedAt   time.Time `json:"updatedAt"`
}
```

**Modified Workspace:**
```go
type Workspace struct {
    ID        string `json:"id"`
    ProjectID string `json:"projectId"`  // NEW: FK to project
    RepoID    string `json:"repoId"`
    Repo      string `json:"repo"`
    Ref       string `json:"ref"`
    // ... other fields unchanged
}
```

### Storage Layout

```
~/.nexus/
├── projects/
│   └── proj-<id>/
│       ├── project.json      # Project metadata
│       └── workspaces/       # Workspace storage
├── daemon.db                 # SQLite: projects + workspaces tables
└── ...
```

---

## CLI Changes

### Modified Commands

**`nexus list`** - New hierarchical output:
```
PROJECT: nexus (github.com/inizio/nexus)
  main-branch      running    firecracker  main
  feature-xyz      stopped    seatbelt     feature/xyz
  fix-bug-123      running    seatbelt     fix/bug-123

PROJECT: dotfiles (/Users/newman/dotfiles)
  dotfiles-dev     running    seatbelt   master

2 projects, 4 workspaces total
```

**`nexus list --flat`** - Original flat format for scripts/backward compat

**`nexus create`** - Implicit project creation:
- Checks if project exists for repo
- Creates project if new repo
- Creates workspace linked to project

### New Commands

**`nexus project list`** - List all projects
**`nexus project show <id>`** - Show project details + workspaces
**`nexus project remove <id>`** - Remove project (and all workspaces)

---

## SDK Changes

### New Types (TypeScript)

```typescript
interface Project {
  id: string;
  name: string;
  primaryRepo: string;
  repoIds: string[];
  createdAt: string;
  updatedAt: string;
}

interface ProjectWithWorkspaces extends Project {
  workspaces: WorkspaceRecord[];
}

interface ProjectListResult {
  projects: Project[];
}
```

### New RPC Methods

- `project.list` - List all projects
- `project.get` - Get project by ID
- `project.remove` - Remove project and workspaces

---

## Migration Strategy

1. **Schema update**: Add `project_id` column to workspaces table (nullable)
2. **Backfill**: Create Project for each unique `repo_id`, link workspaces
3. **Enforce**: Make `project_id` non-nullable
4. **Code update**: Update all workspace creation to link to projects

---

## Future Extensibility

This design enables:

1. **Multi-repo projects**: Add repos to `project.RepoIDs`, workspaces can reference any repo in project
2. **Project-level settings**: Store in `project.json` (default backend, agent profile, etc.)
3. **Project templates**: Predefined project configurations
4. **Cross-repo operations**: Workspaces in same project can share context

---

## Acceptance Criteria

- [ ] `nexus create` auto-creates project for new repos
- [ ] `nexus list` shows hierarchical project/workspace view
- [ ] `nexus list --flat` preserves old output format
- [ ] `nexus project list` shows all projects
- [ ] `nexus project show <id>` displays project + workspaces
- [ ] `nexus project remove <id>` removes project and all workspaces
- [ ] SDK exposes Project types and methods
- [ ] Existing workspaces migrate to projects on first daemon start
- [ ] All tests pass with new schema
