# Repo Decomposition Roadmap

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reshape the repo so major concepts have explicit boundaries, core files stay small and focused, and future features land in predictable modules instead of growing central orchestration files.

**Architecture:** Adopt a boundary-first decomposition strategy across the daemon, SDK, E2E, and docs. Split by concept first (`transport`, `storage`, `runtime-selection`, `workspace-lifecycle`, `auth`, `rpc`, `harness`) and enforce tiered file-size limits inside those boundaries. The highest-leverage work is to thin the daemon edge layer, isolate workspace/runtime orchestration from persistence, unify SDK transport, and make E2E package naming and contracts reflect the actual surface under test.

**Tech Stack:** Go daemon in `packages/nexus`, TypeScript SDK in `packages/sdk/js`, Jest E2E in `packages/e2e/flows`, Markdown docs under `docs/`.

---

## File Structure

**Create:**

- `docs/superpowers/plans/2026-04-11-repo-decomposition-roadmap.md` — canonical roadmap for decomposition policy, target structure, package naming guidance, and phased execution

**Modify:**

- `AGENTS.md` — add or align decomposition rules after the policy is adopted
- `docs/explanation/architecture.md` — describe the new layer boundaries once Phase 1 lands
- `docs/reference/project-structure.md` — reflect the canonical package and folder layout after E2E rename / restructuring
- `packages/nexus/pkg/server/server.go` — shrink to composition and thin routing
- `packages/nexus/pkg/handlers/workspace_manager.go` — move orchestration out into concept packages
- `packages/sdk/js/src/client.ts` — reduce to Node transport adapter over shared RPC core
- `packages/sdk/js/src/browser-client.ts` — reduce to browser transport adapter over shared RPC core
- `packages/sdk/js/src/types.ts` — split into domain-specific type files
- `packages/e2e/flows/package.json` — rename package after path rename
- `packages/e2e/flows/src/**` — move into clearer harness/cases/contracts structure

---

## Naming Recommendation: `packages/e2e/flows`

**Current problem:** `sdk-runtime` describes the implementation path used to drive the tests, not the product surface or contract being verified. The package contains harness smoke, lifecycle hooks, runtime selection, auth forwarding, spotlight compose, config bundle, and UI/CLI parity checks. That is broader than "SDK runtime".

**Recommended rename:** `packages/e2e/flows`

**Why this is better:**

- It names the user-facing action being verified: the end-to-end flows that SDK and CLI entrypoints are expected to produce.
- It stays accurate whether tests are driven by SDK, CLI, or both.
- It accommodates parity, auth, runtime, lifecycle, and tunnel tests without implying they are SDK-specific.
- It reads well as a package name: `@nexus/e2e-flows`.
- It scales: if more E2E suites are needed later (e.g. `packages/e2e/billing-flows`), `flows` remains a clear, consistent sibling convention.

**Other acceptable options:**

- `packages/e2e/workspace-flows` — good if the suite remains scenario-heavy, weaker on contract semantics
- `packages/e2e/workspace-surface` — good if the suite is mostly API/UI parity, slightly less common language
- `packages/e2e/workspace-platform` — broader, but more ambiguous than `flows`

**Do not prefer:**

- `sdk-runtime` — too implementation-specific
- `sdk-contract` — too narrow given CLI and parity responsibilities
- `integration` — too generic to guide ownership

**Decision:** Use `flows` as the package path and `@nexus/e2e-flows` as the package name. If the repo later needs multiple E2E suites, add more specific siblings beside it instead of renaming it again.

---

## Repo-Wide Policy

### Task 1: Adopt Layering and File-Size Guardrails

**Files:**

- Modify: `AGENTS.md`
- Modify: `docs/explanation/architecture.md`
- Modify: `docs/reference/project-structure.md`

- [ ] **Step 1: Adopt tiered file-size limits**

Use this policy in docs and review:

```text
Core/domain logic: <= 300 lines
Orchestration/application logic: <= 400 lines
Transport/adapters/tests: <= 500 lines
Generated files: exempt
```

- [ ] **Step 2: Adopt dependency direction rules**

Document these rules:

```text
domain -> no project-specific dependencies
application/orchestration -> may depend on domain
transport -> may depend on application/domain
storage -> implements domain/application-owned interfaces
tests -> may depend on any layer, but harness code should remain modular
```

- [ ] **Step 3: Adopt concept naming rules**

Document these expectations:

```text
transport/   wire protocol, sockets, adapters, sessions
storage/     persistence and backing stores
runtime/     backend selection, preflight, driver-specific behavior
workspace/   lifecycle, readiness, relations, create/fork/restore flows
auth/        relay, bundle, profile mapping
rpc/         method registration, DTOs, middleware
harness/     reusable e2e support code only
```

- [ ] **Step 4: Add a lightweight CI check for new oversized files**

Run: `python scripts/check_file_sizes.py` or equivalent repo-native script

Expected: New or edited files above the tier limit fail CI unless explicitly allowlisted.

- [ ] **Step 5: Keep current oversized files as tracked debt, not instant failures**

Create a debt list in `docs/explanation/architecture.md` or a dedicated appendix:

```text
packages/nexus/pkg/server/server.go
packages/nexus/pkg/handlers/workspace_manager.go
packages/sdk/js/src/client.ts
packages/sdk/js/src/browser-client.ts
packages/sdk/js/src/types.ts
packages/e2e/flows/src/cases/workspace-manager.test.ts
```

- [ ] **Step 6: Commit**

```bash
git add AGENTS.md docs/explanation/architecture.md docs/reference/project-structure.md scripts/check_file_sizes.py
git commit -m "docs: add decomposition guardrails and file size policy"
```

---

## Daemon Decomposition

### Task 2: Thin the Daemon Edge Layer

**Files:**

- Modify: `packages/nexus/pkg/server/server.go`
- Create: `packages/nexus/pkg/server/rpc/registry.go`
- Create: `packages/nexus/pkg/server/rpc/methods/workspace.go`
- Create: `packages/nexus/pkg/server/rpc/methods/fs.go`
- Create: `packages/nexus/pkg/server/rpc/methods/spotlight.go`
- Create: `packages/nexus/pkg/server/pty/open.go`
- Create: `packages/nexus/pkg/server/pty/io.go`
- Create: `packages/nexus/pkg/server/transport/websocket/session.go`

- [ ] **Step 1: Replace the large `processRPC` switch with a method registry**

Target shape:

```go
type MethodHandler func(msg *RPCMessage, conn *Connection) *RPCResponse

type Registry struct {
    methods map[string]MethodHandler
}

func (r *Registry) Register(name string, handler MethodHandler) { /* ... */ }
func (r *Registry) Dispatch(name string, msg *RPCMessage, conn *Connection) *RPCResponse { /* ... */ }
```

- [ ] **Step 2: Move PTY operations out of `server.go`**

Create focused files for:

```text
packages/nexus/pkg/server/pty/open.go
packages/nexus/pkg/server/pty/write.go
packages/nexus/pkg/server/pty/resize.go
packages/nexus/pkg/server/pty/close.go
```

- [ ] **Step 3: Move websocket/session lifecycle into `transport/websocket`**

Move connection setup, message loop, disconnect handling, and session bookkeeping out of `server.go`.

- [ ] **Step 4: Keep `server.go` as composition only**

`server.go` should wire:

```text
transport
registry
workspace manager
runtime factory
spotlight manager
auth relay broker
```

- [ ] **Step 5: Verify**

Run: `cd packages/nexus && go test ./pkg/server/... ./pkg/handlers/... -v`

Expected: Routing and PTY tests still pass with thinner server composition.

- [ ] **Step 6: Commit**

```bash
git add packages/nexus/pkg/server/
git commit -m "refactor(server): split rpc routing transport and pty handling"
```

---

### Task 3: Separate Workspace Orchestration from Storage and Runtime Selection

**Files:**

- Modify: `packages/nexus/pkg/handlers/workspace_manager.go`
- Create: `packages/nexus/pkg/workspace/create/service.go`
- Create: `packages/nexus/pkg/workspace/create/spec.go`
- Create: `packages/nexus/pkg/runtime/selection/service.go`
- Create: `packages/nexus/pkg/storage/workspace/store.go`
- Create: `packages/nexus/pkg/git/worktree/service.go`

- [ ] **Step 1: Move `workspace.create` orchestration into `workspace/create`**

That service should own:

```text
repo config lookup
runtime preflight
runtime setup/install attempts
driver selection request
workspace record creation call
local runtime workspace initialization
```

- [ ] **Step 2: Create one runtime decision entrypoint**

`runtime/selection/service.go` should become the only place that decides:

```text
required backend constraints
preflight status handling
fallback policy
driver selection inputs
```

- [ ] **Step 3: Split `workspacemgr` responsibilities**

Target ownership:

```text
storage/workspace   -> persistence, sqlite, record access
git/worktree        -> clone/fork/worktree path operations
workspace/lifecycle -> coordination and state transitions
```

- [ ] **Step 4: Move git/path heuristics out of persistence-oriented manager code**

Path guessing, fork creation, and worktree sync should not live beside storage coordination.

- [ ] **Step 5: Verify**

Run: `cd packages/nexus && go test ./pkg/handlers/... ./pkg/workspace/... ./pkg/runtime/... ./pkg/workspacemgr/... -v`

Expected: Runtime selection and workspace lifecycle remain behaviorally identical with clearer ownership.

- [ ] **Step 6: Commit**

```bash
git add packages/nexus/pkg/handlers packages/nexus/pkg/workspace packages/nexus/pkg/runtime/selection packages/nexus/pkg/storage packages/nexus/pkg/git
git commit -m "refactor(workspace): split orchestration storage and runtime selection"
```

---

### Task 4: Normalize Runtime Driver Shared Logic

**Files:**

- Modify: `packages/nexus/pkg/runtime/seatbelt/driver.go`
- Modify: `packages/nexus/pkg/runtime/lxc/driver.go`
- Create: `packages/nexus/pkg/runtime/drivers/shared/session.go`
- Create: `packages/nexus/pkg/runtime/drivers/shared/bootstrap.go`

- [ ] **Step 1: Extract shared guest-session helpers**

Move repeated logic for:

```text
instance naming
shell spawn
bootstrap-once guards
common retry behavior
shared path/env preparation
```

- [ ] **Step 2: Keep backend-specific drivers focused on backend differences**

Driver packages should mostly describe:

```text
how this backend boots
how it mounts
how it executes
what capabilities it exposes
```

- [ ] **Step 3: Verify**

Run: `cd packages/nexus && go test ./pkg/runtime/... -v`

Expected: Driver behavior preserved while shared code duplication drops.

- [ ] **Step 4: Commit**

```bash
git add packages/nexus/pkg/runtime/
git commit -m "refactor(runtime): extract shared driver session helpers"
```

---

## SDK Decomposition

### Task 5: Unify RPC Transport and Split Types by Domain

**Files:**

- Modify: `packages/sdk/js/src/client.ts`
- Modify: `packages/sdk/js/src/browser-client.ts`
- Modify: `packages/sdk/js/src/workspace-handle.ts`
- Create: `packages/sdk/js/src/rpc/connection.ts`
- Create: `packages/sdk/js/src/rpc/request-map.ts`
- Create: `packages/sdk/js/src/rpc/notifications.ts`
- Create: `packages/sdk/js/src/transport/node-websocket.ts`
- Create: `packages/sdk/js/src/transport/browser-websocket.ts`
- Create: `packages/sdk/js/src/types/workspace.ts`
- Create: `packages/sdk/js/src/types/exec.ts`
- Create: `packages/sdk/js/src/types/fs.ts`
- Create: `packages/sdk/js/src/types/pty.ts`
- Create: `packages/sdk/js/src/types/spotlight.ts`

- [ ] **Step 1: Create a shared internal RPC core**

It should own:

```text
request IDs
pending request map
notification dispatch
message parsing
reconnect policy
```

- [ ] **Step 2: Reduce `client.ts` and `browser-client.ts` to transport adapters**

Node and browser code should only differ in websocket implementation details.

- [ ] **Step 3: Introduce one shared `RPCClient` interface**

Remove duplicated local interfaces and unsafe casts like:

```ts
this.execOps = new ExecOperations(client as never, scopedParams);
this.fsOps = new FSOperations(client as never, scopedParams);
```

- [ ] **Step 4: Split `types.ts` by domain**

Use:

```text
types/workspace.ts
types/exec.ts
types/fs.ts
types/pty.ts
types/spotlight.ts
rpc/types.ts
```

- [ ] **Step 5: Verify**

Run: `cd packages/sdk/js && pnpm test && pnpm build`

Expected: Node and browser clients still behave consistently, and type boundaries are simpler.

- [ ] **Step 6: Commit**

```bash
git add packages/sdk/js/src
git commit -m "refactor(sdk): unify rpc transport and split types by domain"
```

---

## E2E Package Rename and Harness Restructure

### Task 6: Rename `sdk-runtime` to `flows`

**Files:**

- Move: `packages/e2e/sdk-runtime` -> `packages/e2e/flows`
- Modify: `packages/e2e/flows/package.json`
- Modify: repo docs and any workspace config or CI references to `@nexus/e2e-sdk-runtime`

- [ ] **Step 1: Rename the package directory**

Move:

```text
packages/e2e/sdk-runtime
packages/e2e/flows
```

- [ ] **Step 2: Rename the package**

Set:

```json
{
  "name": "@nexus/e2e-flows"
}
```

- [ ] **Step 3: Update all references**

Search for:

```text
sdk-runtime
@nexus/e2e-sdk-runtime
packages/e2e/sdk-runtime
```

Replace with the new name everywhere relevant.

- [ ] **Step 4: Verify**

Run: `rg "sdk-runtime|@nexus/e2e-sdk-runtime|packages/e2e/sdk-runtime" .`

Expected: No stale references remain except historical docs or transcripts intentionally left untouched.

- [ ] **Step 5: Commit**

```bash
git add packages/e2e docs
git commit -m "refactor(e2e): rename sdk-runtime package to flows"
```

---

### Task 7: Split Harness, Cases, and Contract Ownership

**Files:**

- Modify: `packages/e2e/flows/src/harness/*.ts`
- Modify: `packages/e2e/flows/src/cases/*.test.ts`
- Create: `packages/e2e/flows/src/harness/daemon/`
- Create: `packages/e2e/flows/src/harness/repo/`
- Create: `packages/e2e/flows/src/harness/session/`
- Create: `packages/e2e/flows/src/parity/contracts/`

- [ ] **Step 1: Split reusable harness code by concept**

Target shape:

```text
harness/daemon/
harness/repo/
harness/session/
harness/assertions/
harness/fixtures/
```

- [ ] **Step 2: Remove duplicated helpers**

Deduplicate:

```text
repo root detection
session startup
daemon startup mode selection
workspace create error assertions
fixture repo creation
```

- [ ] **Step 3: Make parity scope explicit**

Choose one:

```text
A. Expand the parity matrix to include all product-critical case IDs
B. Keep parity narrower, but clearly separate parity contracts from extended scenario suites
```

Recommended: `B` first, then expand later once ownership is clearer.

- [ ] **Step 4: Classify suites by intent**

Use labels or folders such as:

```text
parity/
runtime/
auth/
lifecycle/
spotlight/
config/
live/
```

- [ ] **Step 5: Verify**

Run: `cd packages/e2e/flows && pnpm test`

Expected: Same behavioral coverage, less copied harness logic, clearer suite ownership.

- [ ] **Step 6: Commit**

```bash
git add packages/e2e/flows/src
git commit -m "refactor(e2e): split workspace contract harness and suite ownership"
```

---

## Docs and Scaffolding

### Task 8: Align Docs and Canonical Scaffolding

**Files:**

- Modify: `AGENTS.md`
- Modify: `docs/explanation/architecture.md`
- Modify: `docs/reference/project-structure.md`
- Modify: `docs/dev/contributing.md`
- Modify: `.nexus/**` (repository root only)

- [ ] **Step 1: Align docs with the actual repo structure**

Reflect:

```text
docs/explanation/
docs/reference/
docs/superpowers/
packages/e2e/flows/
```

- [ ] **Step 2: Make `.nexus/` ownership explicit**

Canonical scaffold is **only** at the repository root:

```text
.nexus/    # repo root
```

The duplicate under `packages/nexus/` was removed to avoid drift; do not reintroduce it.

- [ ] **Step 3: Document where new code belongs**

Add a short placement guide:

```text
transport code goes in transport/
storage code goes in storage/
workspace lifecycle code goes in workspace/
shared runtime helpers go in runtime/drivers/shared/
e2e support code goes in harness/
```

- [ ] **Step 4: Verify**

Run: `pnpm test` for docs-linked packages as needed, plus a docs link check if available.

Expected: Docs match the real layout and new contributors can place code without guesswork.

- [ ] **Step 5: Commit**

```bash
git add AGENTS.md docs .nexus
git commit -m "docs: align architecture project structure and scaffold ownership"
```

---

## Recommended Execution Order

1. Task 1: policy and guardrails
2. Task 2: daemon edge split
3. Task 3: workspace/runtime/storage separation
4. Task 5: SDK transport and types
5. Task 6: E2E rename
6. Task 7: E2E harness restructure
7. Task 4: runtime driver shared layer
8. Task 8: docs and scaffolding alignment

Reasoning:

- Guardrails prevent regression while refactors are underway.
- The daemon edge and workspace lifecycle are the biggest current bottlenecks.
- SDK transport unification removes duplicated maintenance load.
- Renaming E2E before deeper harness changes avoids doing two rounds of path churn.
- Runtime driver sharing is valuable but less urgent than the boundary cleanup above it.

---

## Acceptance Checklist

The program is complete when these statements are true:

- No central production file mixes transport, orchestration, and storage concerns.
- New product features can usually land without editing a giant switch or mega-manager file.
- Core logic files are generally under `300-400` lines, with transport/tests under `500`.
- The daemon has explicit module boundaries for `server`, `rpc`, `pty`, `workspace`, `runtime`, `storage`, `git`, and `auth`.
- The SDK has one RPC core and domain-specific type modules.
- The E2E package name tells contributors what contract it verifies.
- Docs and scaffolding describe the actual repo rather than a past version of it.

---

## Self-Review

**Spec coverage:**

- Covers repo-wide file-size and layer policy
- Covers daemon/server decomposition
- Covers workspace/runtime/storage separation
- Covers SDK transport and type decomposition
- Covers E2E rename and harness restructuring
- Covers docs and scaffold alignment

**Placeholder scan:** No `TBD`/`TODO` placeholders remain.

**Type consistency:** The recommended E2E rename is consistently `flows` / `@nexus/e2e-flows` throughout the roadmap.
