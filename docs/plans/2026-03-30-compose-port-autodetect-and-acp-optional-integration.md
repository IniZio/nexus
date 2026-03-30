# Compose Port Autodetect and ACP Optional Integration Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Make Nexus auto-detect and forward all docker-compose published ports by default, while integrating OpenCode ACP as an optional capability without weakening security.

**Architecture:** Add a compose port discovery path in the workspace daemon and expose it via a narrow Spotlight helper RPC. The daemon will parse compose service port bindings and apply Spotlight forwards for every published binding, defaulting to loopback host forwarding behavior already enforced by Spotlight. ACP is treated as an optional machine capability: detected at runtime, never required for workspace readiness unless explicitly requested.

**Tech Stack:** Go (workspace-daemon + tests), TypeScript (workspace-sdk + tests), Docker Compose CLI, JSON-RPC.

---

### Task 1: Add compose port discovery primitive in daemon

**Files:**
- Create: `packages/workspace-daemon/pkg/compose/discovery.go`
- Create: `packages/workspace-daemon/pkg/compose/discovery_test.go`

**Step 1: Write failing tests for compose file detection and published port extraction**

Test cases:
- detects `docker-compose.yml` in workspace root
- detects `docker-compose.yaml` in workspace root
- parses `ports` entries and returns all published host/container mappings
- returns empty result when no compose file exists

**Step 2: Run tests to verify failure**

Run: `cd packages/workspace-daemon && go test ./pkg/compose -v`
Expected: FAIL (package not implemented yet).

**Step 3: Implement discovery API**

Implement API shape:

```go
type PublishedPort struct {
    Service    string
    HostIP     string
    HostPort   int
    TargetPort int
    Protocol   string
}

func DiscoverPublishedPorts(ctx context.Context, workspaceRoot string) ([]PublishedPort, error)
```

Implementation details:
- resolve compose file path in root (`docker-compose.yml`, then `docker-compose.yaml`)
- invoke `docker compose -f <file> config --format json`
- parse `services.*.ports` entries
- include all published mappings (no subset filtering)
- normalize host IP empty to `127.0.0.1` behavior at Spotlight call site

**Step 4: Add robust fallback parser behavior**

If `--format json` is unavailable, fallback to `docker compose -f <file> config` and return a clear structured error (do not silently mis-parse).

**Step 5: Run tests**

Run: `cd packages/workspace-daemon && go test ./pkg/compose -v`
Expected: PASS.

**Step 6: Commit**

```bash
git add packages/workspace-daemon/pkg/compose
git commit -m "feat(compose): add published port discovery"
```

### Task 2: Add Spotlight helper RPC to apply compose-discovered ports

**Files:**
- Modify: `packages/workspace-daemon/pkg/handlers/spotlight.go`
- Modify: `packages/workspace-daemon/pkg/handlers/spotlight_test.go`
- Modify: `packages/workspace-daemon/pkg/server/server.go`

**Step 1: Write failing handler tests for compose auto-forward action**

Test cases:
- `spotlight.applyComposePorts` forwards all discovered mappings
- collisions are reported per mapping (skip failed mapping, continue others)
- no compose file returns empty forward list with non-fatal response

**Step 2: Run targeted tests to verify failure**

Run: `cd packages/workspace-daemon && go test ./pkg/handlers -run Spotlight -v`
Expected: FAIL (new RPC action not routed/implemented).

**Step 3: Implement handler and response shape**

Add RPC:
- `spotlight.applyComposePorts`

Request:
- `workspaceId`
- `rootPath`

Response:
- `forwards`: successfully created forwards
- `errors`: per-port errors with service and host/target ports

Apply logic:
- call `compose.DiscoverPublishedPorts`
- for each discovered published mapping, call Spotlight `Expose`
- preserve existing host binding behavior; default host `127.0.0.1`

**Step 4: Route method in server switch**

Modify `processRPC` method switch in `server.go` to route `spotlight.applyComposePorts`.

**Step 5: Run tests**

Run: `cd packages/workspace-daemon && go test ./pkg/handlers -run Spotlight -v`
Expected: PASS.

**Step 6: Commit**

```bash
git add packages/workspace-daemon/pkg/handlers/spotlight.go packages/workspace-daemon/pkg/handlers/spotlight_test.go packages/workspace-daemon/pkg/server/server.go
git commit -m "feat(spotlight): add compose port auto-forward action"
```

### Task 3: Add SDK wrapper for compose auto-forward helper

**Files:**
- Modify: `packages/workspace-sdk/src/types.ts`
- Modify: `packages/workspace-sdk/src/spotlight.ts`
- Modify: `packages/workspace-sdk/src/workspace-handle.ts`
- Modify: `packages/workspace-sdk/src/__tests__/spotlight.test.ts`

**Step 1: Write failing SDK test for compose apply helper**

Add test:
- calls `ws.spotlight.applyComposePorts()`
- verifies typed response includes forwards and errors arrays

**Step 2: Run SDK test to verify failure**

Run: `cd packages/workspace-sdk && pnpm exec jest src/__tests__/spotlight.test.ts --runInBand`
Expected: FAIL (method/types missing).

**Step 3: Implement SDK method and types**

Add:
- `applyComposePorts(): Promise<{ forwards: SpotlightForward[]; errors: SpotlightApplyError[] }>`

Ensure method sends `workspaceId` and resolved root path payload required by daemon.

**Step 4: Run SDK test**

Run: `cd packages/workspace-sdk && pnpm exec jest src/__tests__/spotlight.test.ts --runInBand`
Expected: PASS.

**Step 5: Commit**

```bash
git add packages/workspace-sdk/src/types.ts packages/workspace-sdk/src/spotlight.ts packages/workspace-sdk/src/workspace-handle.ts packages/workspace-sdk/src/__tests__/spotlight.test.ts
git commit -m "feat(workspace-sdk): add compose spotlight auto-forward api"
```

### Task 4: Add ACP capability detection and optional readiness behavior

**Files:**
- Modify: `packages/workspace-daemon/pkg/handlers/workspace_ready.go`
- Modify: `packages/workspace-daemon/pkg/handlers/workspace_ready_test.go`
- Modify: `packages/workspace-daemon/pkg/server/server.go`

**Step 1: Write failing tests for optional ACP check behavior**

Test cases:
- ACP check command succeeds when `opencode` exists and process is running
- ACP check is treated optional when machine has no `opencode` binary
- default profile no longer hard-fails on missing ACP capability

**Step 2: Run targeted readiness tests to verify failure**

Run: `cd packages/workspace-daemon && go test ./pkg/handlers -run WorkspaceReady -v`
Expected: FAIL (new ACP optional semantics not yet implemented).

**Step 3: Implement capability detection helper**

Add helper in readiness logic:
- detect `opencode` presence via command existence
- if missing, mark ACP capability unavailable and treat ACP-specific checks as pass-skipped unless explicitly strict

Keep strict behavior for non-ACP checks.

**Step 4: Ensure security boundary remains intact**

Do not expose ACP endpoint directly from daemon. Keep access within existing workspace auth + RPC and local port forwarding mechanisms only.

**Step 5: Run readiness tests**

Run: `cd packages/workspace-daemon && go test ./pkg/handlers -run WorkspaceReady -v`
Expected: PASS.

**Step 6: Commit**

```bash
git add packages/workspace-daemon/pkg/handlers/workspace_ready.go packages/workspace-daemon/pkg/handlers/workspace_ready_test.go packages/workspace-daemon/pkg/server/server.go
git commit -m "feat(ready): make opencode acp checks capability-aware"
```

### Task 5: Add convention-first auto-apply flow for compose ports

**Files:**
- Modify: `packages/workspace-daemon/pkg/server/server.go`
- Modify: `packages/workspace-daemon/pkg/handlers/workspace_manager.go`
- Modify: `packages/workspace-daemon/pkg/handlers/workspace_manager_test.go`

**Step 1: Write failing tests for automatic compose forward apply trigger**

Behavior:
- when workspace root contains compose file, Nexus auto-applies compose forwards as part of workspace startup/ready workflow
- when no compose file, behavior is no-op

**Step 2: Run targeted tests to verify failure**

Run: `cd packages/workspace-daemon && go test ./pkg/handlers -run Workspace -v`
Expected: FAIL.

**Step 3: Implement auto-trigger integration point**

Recommended trigger:
- on `workspace.ready` invocation, after successful readiness checks, call compose auto-forward apply once per workspace session (idempotent cache/guard)

Implementation requirements:
- idempotent: avoid duplicate forwarding attempts each ready poll
- non-fatal if compose discovery fails; include diagnostics in logs

**Step 4: Run tests**

Run: `cd packages/workspace-daemon && go test ./pkg/handlers -run Workspace -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add packages/workspace-daemon/pkg/server/server.go packages/workspace-daemon/pkg/handlers/workspace_manager.go packages/workspace-daemon/pkg/handlers/workspace_manager_test.go
git commit -m "feat(workspace): auto-apply compose spotlight forwards by convention"
```

### Task 6: Update documentation to reflect convention-over-configuration behavior

**Files:**
- Modify: `docs/reference/workspace-daemon.md`
- Modify: `docs/reference/workspace-sdk.md`
- Modify: `docs/reference/workspace-config.md`

**Step 1: Document compose auto-detect behavior**

Add:
- compose file detection locations
- “all published ports forwarded” default
- error/collision behavior

**Step 2: Document ACP optional capability flow**

Add:
- `opencode` presence detection
- ACP checks skipped when capability absent
- no insecure endpoint exposure

**Step 3: Reduce required config examples**

Adjust docs so `.nexus/workspace.json` is optional for common compose projects and used for overrides/advanced behavior only.

**Step 4: Run mention-sanity check**

Run: `grep -RIn "hanlun" docs/reference || true`
Expected: no hard-coded private project assumptions in general docs.

**Step 5: Commit**

```bash
git add docs/reference/workspace-daemon.md docs/reference/workspace-sdk.md docs/reference/workspace-config.md
git commit -m "docs: describe compose auto-forward and optional acp capability"
```

### Task 7: Add case-study verification in local hanlun clone (non-repo-affecting)

**Files:**
- Verify in: `.nexus/case-studies/hanlun-lms/`

**Step 1: Ensure case-study repo is present and clean branch available**

Run:
- `cd .nexus/case-studies/hanlun-lms && git status --short --branch`

**Step 2: Validate compose detection path**

Run:
- invoke new Spotlight compose helper through SDK or RPC test harness
- verify expected ports include 5173, 5174, 8000 and other published ports

**Step 3: Validate ACP optional behavior**

Run in two modes:
- with `opencode` available: ACP capability present
- simulated unavailable binary path: ACP checks skipped without hard fail

**Step 4: Capture verification notes**

Record findings in development notes (not user-facing docs) including collisions and any required follow-up.

**Step 5: Commit (if repository files changed)**

Only if Nexus repo files changed for this task; do not commit unrelated case-study artifacts.

### Task 8: Final verification gate

**Files:**
- Modify: any touched files from tasks above (if fixes needed)

**Step 1: Run daemon tests**

Run: `cd packages/workspace-daemon && go test ./...`
Expected: PASS.

**Step 2: Run SDK checks/tests**

Run:
- `cd packages/workspace-sdk && pnpm exec tsc --noEmit`
- `cd packages/workspace-sdk && pnpm exec jest --runInBand`

Expected: PASS.

**Step 3: Run repository CI**

Run: `task ci`
Expected: PASS, or document environment blockers with exact error evidence.

**Step 4: Final consistency check**

Run:
- `git status --short`
- `git diff --stat`

Ensure no accidental changes in `.nexus/case-studies/hanlun-lms` are included unless intentionally committed.

**Step 5: Final commit**

```bash
git add -A
git commit -m "feat(workspace): convention-first compose port forwarding and optional acp integration"
```
