# Remote Workspace Core Design (Minimal API)

Date: 2026-03-30
Status: Proposed and approved in design review
Audience: Internal Nexus maintainers (not external SDK consumers yet)

## 1. Goal

Build a remote-first workspace core that provides full agent isolation and still feels as smooth as local development.

This design intentionally centers on workspace runtime boundaries and lifecycle reliability, not task orchestration and not Boulder enforcement.

## 2. Product Direction

- Remote-only execution model.
- No local execution backend as part of the core path.
- No git worktree dependency in core design.
- Internal API surface stays minimal and opinionated.
- Workspace means a full execution boundary:
  - git/code boundary
  - process/runtime boundary
  - credentials/auth boundary

## 3. Non-Goals (Core)

- Task queue or task orchestration semantics.
- Boulder integration requirements.
- Exposing transport protocol details as public API.
- External/public SDK stability guarantees in v1.

## 4. Implemented vs Missing (Current Snapshot)

Current repository state indicates:

- Implemented:
  - Enforcer core and plugin integrations are present.
  - Worktree isolation direction is already captured in ADR-001.
- Partially implemented / in progress:
  - Workspace daemon and workspace SDK exist but are documented as not fully implemented.
- Missing for target outcome:
  - A stable remote-first workspace contract with full lifecycle management (setup/start/ready/teardown), credential bridges, and ergonomic port forwarding.

## 5. Minimal Core API

### 5.1 Workspace manager

Only these entry points are part of the core contract:

- `workspace.create(spec) -> WorkspaceHandle`
- `workspace.open(id) -> WorkspaceHandle`
- `workspace.list(filter?) -> WorkspaceSummary[]`
- `workspace.remove(id, { force?: boolean })`

### 5.2 Workspace handle

`WorkspaceHandle` intentionally exposes only essential capabilities:

- `handle.exec(command, opts?)`
- `handle.fs.readFile(path, encoding?)`
- `handle.fs.writeFile(path, content)`
- `handle.fs.readdir(path)`
- `handle.fs.stat(path)`
- `handle.fs.mkdir(path, opts?)`
- `handle.fs.rm(path, opts?)`
- `handle.git.status()`
- `handle.git.diff(opts?)`
- `handle.git.add(paths?)`
- `handle.git.commit(message, opts?)`
- `handle.git.revParse(ref)`
- `handle.git.checkout(refOrBranch, opts?)`
- `handle.service.start(name, opts?)`
- `handle.service.stop(name, opts?)`
- `handle.service.status(name?)`
- `handle.service.logs(name, opts?)`
- `handle.info()`

### 5.3 Spotlight (local dev port experience)

Spotlight is a first-class capability for local testing ergonomics:

- `handle.spotlight.expose({ service, remotePort, localPort, host? })`
- `handle.spotlight.list()`
- `handle.spotlight.close(id)`

Default Spotlight profile for internal-repo:

- `student-portal`: remote `5173` -> local `5173`
- `api`: remote `8000` -> local `8000`

Collision behavior:

- Default: fail fast with clear actionable error.
- Optional mode: `autoRemap` returns the assigned local port.

## 6. Create Spec (Required and Optional)

`workspace.create(spec)` fields:

Required:

- `repo` (e.g. `<internal-repo-url>`)
- `ref` (branch/tag/sha)
- `workspaceName`
- `agentProfile`

Optional:

- `authProfiles[]` (e.g. `claude-auth`, `codex-auth`, `gitconfig`)
- `sshAgentForward` (boolean)
- `gitCredentialMode` (`host-helper` | `ephemeral-helper` | `none`)
- `envAllowlist[]`
- `resourceLimits` (cpu/memory/timeouts)
- `networkPolicy` (`default` | `restricted`)

## 7. Lifecycle Contract

Workspace lifecycle phases must be explicit and queryable:

1. `setup` - clone/fetch, checkout ref, run bootstrap hooks
2. `start` - launch required services (including `opencode serve`/ACP)
3. `ready` - health checks pass, endpoints discoverable
4. `active` - agent operations in progress
5. `teardown` - stop services, flush logs/artifacts, revoke forwards/mounts, cleanup runtime resources

State transitions must be idempotent where practical and safe to resume after transient failures.

## 8. Credential and Auth Bridge Policy

Credentials are policy-bound at workspace creation and reported via `handle.info()`.

- Auth folder sync/mount is profile-based and explicit.
- SSH agent forwarding is opt-in.
- Git credential behavior is explicit (`host-helper`, `ephemeral-helper`, or `none`).
- No implicit broad host-path mounts.

## 9. Remote-Only Architecture Stance

The runtime is always remote. The client API remains backend-agnostic, but callers do not select a local backend.

Architecture preference:

- Thin client + remote control plane/daemon.
- Keep transport and orchestration internals private.

## 10. Superpowers/Skills Integration Requirement

For implementation-oriented agent skills, workspace binding is mandatory:

- acquire/create workspace first
- root all exec/fs/git/service operations to that workspace
- teardown (or explicit retention policy) at session end

This ensures deterministic isolation and eliminates shared local cwd drift across concurrent agents.

## 11. Local-Feel UX SLOs

Primary experience targets:

- Fast attach/open in steady-state.
- Spotlight forwards establish quickly and survive transient reconnects.
- Predictable workspace paths and service addressing.
- Teardown leaves no leaked services, forwards, or stale auth mounts.

SLOs should be tracked as p50/p95 metrics over repeated runs.

## 12. Battle-Test Program: internal-repo

Canonical validation repository:

- `<internal-repo-url>`

Battle-test workflow:

1. Provision multiple remote workspaces in parallel.
2. Run automated setup in each workspace.
3. Start `opencode serve` or ACP endpoints per workspace.
4. Use `web-prototype` as design reference and implement in `web`.
5. Validate Spotlight mappings (`5173`, `8000`) for local testing ergonomics.
6. Repeatedly execute setup/start/ready/teardown cycles to detect flakiness and resource leaks.

Success criteria:

- High setup/start success rate across parallel runs.
- Stable agent interactions against each isolated workspace endpoint.
- Deterministic teardown with no residual processes/forwards.
- Comparable developer feel to local workflows.

## 13. YAGNI Guardrails

To keep API surface minimal:

- Do not add task objects to workspace core.
- Do not expose raw JSON-RPC methods publicly.
- Do not expose backend-specific tuning unless required by repeated production evidence.
- Keep Spotlight and service abstractions narrow and workflow-driven.

## 14. Next Step

Convert this design into an implementation plan focused on:

- remote lifecycle manager
- workspace policy model (auth/ssh/git creds)
- Spotlight forwarding manager
- internal-repo dogfooding harness for repeated lifecycle validation
