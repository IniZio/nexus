# Agent Guidelines

## Project Overview

This repository is the Nexus remote workspace core.

### Components

| Component | Status | Description |
|-----------|--------|-------------|
| **Workspace Daemon** | ✅ Active | Go server for workspace lifecycle, RPC handlers, services, spotlight |
| **Workspace SDK** | ✅ Active | TypeScript SDK for remote workspace control |

### Packages

| Package | Status | Description |
|---------|--------|-------------|
| `packages/nexus` | ✅ Active | Go daemon runtime |
| `packages/sdk/js` | ✅ Active | TypeScript SDK (`@nexus/sdk`) |

### Scope Notes

- Keep repository changes centered on `packages/nexus` and `packages/sdk/js`.
- Do not reintroduce removed non-core package surfaces.

---

## Enforcement Rules

- An agent MUST complete tasks fully before claiming completion.
- An agent MUST verify all requirements are explicitly addressed.
- An agent MUST ensure code works, builds, runs, and tests pass.
- An agent MUST provide evidence of success, not just claims.
- An agent SHOULD test changes in real environments via dogfooding.
- An agent MUST verify builds succeed before claiming completion.
- An agent MUST verify there are zero type errors.
- An agent MUST verify there are zero lint errors.
- An agent SHOULD log friction points encountered during development.
- An agent MUST use isolated workspaces for feature development.
- An agent MUST NOT work directly in the main worktree for features.
- An agent MUST list what remains undone if stopping early.
- An agent MUST explain why it cannot complete a task if stopping early.
- An agent MUST specify what the user needs to do next if stopping early.

---

## Documentation Guidelines

### User-Facing Docs (docs/)

User-facing documentation goes in `docs/` and its subdirectories:

- `docs/tutorials/` - Step-by-step guides for users
- `docs/reference/` - API references, CLI commands, configuration
- `docs/explanation/` - Conceptual explanations and architecture
- `docs/dev/` - Developer documentation (contributing, roadmap, ADRs)

**Only document ACTUALLY IMPLEMENTED features.**

### Internal Docs (docs/dev/internal/)

Historical, planning, and research documents go in `docs/dev/internal/`:

- `docs/dev/internal/implementation/` - Implementation plans (historical)
- `docs/dev/internal/plans/` - Feature plans (some may not be implemented)
- `docs/dev/internal/design/` - Design documents (historical)
- `docs/dev/internal/research/` - Research findings
- `docs/dev/internal/testing/` - Testing reports (some reference unimplemented features)
- `docs/dev/internal/ARCHIVE/` - Archived documents

### Architecture Decision Records

ADRs go in `docs/dev/decisions/`:

- `docs/dev/decisions/001-worktree-isolation.md`
- `docs/dev/decisions/002-port-allocation.md`

### What NOT to Reference

Never reference removed module surfaces as active capabilities.

If a feature doesn't exist, don't document it as if it does. Instead, note it as planned/future.

### Documentation Structure

```
docs/
├── index.md
├── reference/
│   ├── workspace-daemon.md
│   ├── workspace-sdk.md
│   └── workspace-config.md
└── dev/
    ├── contributing.md
    └── migration-core-prune.md
```
