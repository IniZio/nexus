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

## Remote-First Architecture Constraint

**The daemon will run on a machine different from the user's machine.** Every feature must be designed and verified under this assumption.

Concretely this means:

- **Filesystem paths on the daemon host are not the user's paths.** `os.UserHomeDir()` on the daemon returns the server's home, not the user's. Never read credential or config files from the daemon's own `$HOME` and assume they belong to the user.
- **Host-path symlinks are local-only.** Lima's automount makes symlinks work today when daemon and VM share the same host, but this breaks the moment the daemon is remote. Any symlink-based credential forwarding is a temporary local shortcut, not a general solution.
- **All user data must travel via RPC.** Credential files, config bundles, auth tokens — anything originating from the user's machine must be sent explicitly through the `workspace.create` spec or a dedicated upload RPC, not read opportunistically from the daemon host's filesystem.
- **Auth relay tokens are the correct remote-safe pattern for API keys.** `mintAuthRelay` injects tokens at exec time over the existing RPC path and works regardless of where the daemon runs.
- **Known gap (must fix before remote deployment):** `CreateSpec` currently has no `ConfigBundle` field. Both drivers read `os.UserHomeDir()` on the daemon host to build credential bundles. Before enabling a remote daemon, this must be replaced: the client constructs the bundle locally and passes it in `CreateSpec.ConfigBundle`; the daemon never reads user credentials from its own filesystem.

An agent MUST flag any new feature that reads from the daemon's local filesystem for user-owned data as non-compliant with this constraint.

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
