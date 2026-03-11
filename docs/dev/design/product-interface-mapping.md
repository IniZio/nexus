# Product Interface Mapping (Internal)

## Purpose

Map current workspace-era implementation surfaces to the approved canonical nouns:

`Org -> Project -> Branch -> Version -> Environment`

This is an internal design reference. It is not a user-facing command contract.

## Current Implementation Baseline

- Workspace remains the implemented user and internal unit in CLI, HTTP routes, and persisted state.
- Current CLI shape is workspace-first (`nexus workspace create|start|stop|delete|list|ssh|exec|inject-key|status|logs|use|checkpoint`).

## Mapping Table

| Current Surface | Current Meaning | Canonical Direction | Notes |
| --- | --- | --- | --- |
| workspace object | mutable dev unit with runtime + state | branch-owned dev session | transition keeps behavior first, naming later |
| worktree/branch coupling | workspace can carry a branch/worktree path | branch is primary mutable line | one branch maps to one active dev session |
| checkpoint | mutable dev-time snapshot of workspace | version boundary precursor | versions become immutable deploy-only outputs |
| backend runtime (docker/daytona) | execution substrate | remote host dev session runtime | remote host model remains required |
| deployment language | mostly absent in current CLI | version -> environment via deploy | environment/deploy stay downstream of development |
| box internals | runtime execution unit | internal-only substrate term | Box is internal and not user-visible |

## Invariants To Preserve During Migration

- Box is internal implementation detail and never exposed as a product noun.
- Development sessions always execute on a remote host.
- one active dev session per branch.
- Parallel work is fork-first: create forked branches, then run separate sessions per fork.
- Versions are immutable and deploy-only.

## Boundary Definitions

### Current workspace object

- Source touchpoints include `packages/nexusd/internal/cli/workspace.go`, `packages/nexusd/pkg/server/server.go`, and `packages/nexusd/internal/state/store.go`.
- Workspace currently mixes lifecycle, runtime, and identity concerns that future branch/dev-session surfaces should separate.

### Current worktree and branch coupling

- `packages/nexusd/internal/git/worktree.go` models local git worktrees and branch creation.
- Future branch model keeps git branch semantics primary and treats runtime session as attached state.

### Future branch-owned dev session

- A branch owns at most one active dev session instance at a time.
- Session lifecycle aligns to `idle -> active -> suspended -> active -> archived`.

### Future version creation boundary

- Version creation occurs from branch state at explicit cut points.
- Resulting versions are immutable and deploy-only artifacts, not mutable workspaces.

### Future environment/deployment boundary

- Environments receive versions through deployment actions.
- Environment operations do not target mutable branch session state.

### Where Box fits

- Box remains an internal execution/container abstraction.
- Internal docs may mention Box for implementation mapping, but external docs and examples should not.
