# Workspace Started-Only Access Gating

Date: 2026-04-09
Status: Proposed (approved sections in chat)

## Context

Current behavior allows opening interactive access (for example PTY) while a workspace is still in `created` state. This conflicts with the required contract:

- A workspace is only accessible after it is `started`.

The codebase already exposes a server RPC path for `workspace.start`, but the CLI currently does not provide a `workspace start` command. This creates both a policy gap (access before start) and an operator UX gap (no first-class CLI transition to started).

## Goal

Enforce a strict state boundary so interactive access is denied until `started`, while adding an explicit CLI transition path that matches the server model.

## Non-Goals

1. Auto-starting workspaces on first access request.
2. Redesigning lifecycle semantics beyond access gating.
3. Expanding spotlight automation scope in this change.

## Decision Summary

1. Add server-side access gating that requires workspace state `started` for interactive/open-access endpoints.
2. Add `nexus workspace start <id>` CLI support that invokes `workspace.start` RPC.
3. Keep state transitions explicit; access attempts must not mutate state.
4. Standardize denial errors so CLI/UI can provide clear next actions.

## Architecture

### Access Gate Boundary

Create a shared state guard in server request handling for interactive entry points, including:

- PTY open/session attach paths
- Portal/session attach paths
- Any equivalent workspace access bridge where a user can interact with runtime

The guard must:

1. Resolve workspace by ID.
2. Check current state.
3. Deny unless state is `started`.
4. Return a consistent machine-readable error payload.

### CLI Start Command

Add `workspace start` subcommand to `nexus workspace` command tree:

- `nexus workspace start <workspace-id>`

Behavior:

1. Call `workspace.start` RPC.
2. Surface success/failure clearly.
3. Preserve existing command behavior for list/create/stop/remove/fork/portal.

## Request and State Flow

1. `workspace create` results in `created` state.
2. Access request while `created` is denied with `workspace_not_started` style error and no side effects.
3. `workspace start <id>` transitions to `started`.
4. Access request after transition succeeds.
5. Stop/remove semantics remain unchanged.

## Error Model

Access denial should be stable and debuggable:

- error code: stable value suitable for programmatic handling
- message: clear user-facing summary (`workspace not started`)
- metadata: workspace id + current state
- guidance: caller-facing hint to run `nexus workspace start <id>`

## Testing Strategy

### Unit and Integration Tests

1. Server tests for each guarded endpoint:
   - deny in `created`
   - allow in `started`
2. Regression test that denied access does not transition state.
3. CLI tests for `workspace start`:
   - validates argument handling
   - calls correct RPC
   - propagates failure details

### Runtime Verification on Port 8080

Verification sequence:

1. Start daemon on `:8080`.
2. Create workspace and record ID.
3. Attempt PTY/portal access before start, confirm denial.
4. Run `nexus workspace start <id>`.
5. Retry PTY/portal access, confirm success.
6. Capture concise command and log evidence in report.

## Risks and Mitigations

1. Missed access endpoint bypasses policy.
   - Mitigation: central shared guard and endpoint inventory in tests.
2. CLI/server error shape mismatch.
   - Mitigation: assert expected error code/message mapping in CLI tests.
3. Existing scripts relying on implicit access-in-created behavior break.
   - Mitigation: explicit error guidance and release-note callout.

## Success Criteria

1. No interactive workspace access is possible before `started`.
2. `nexus workspace start <id>` is available and functional.
3. Access attempts in `created` fail with consistent actionable error.
4. Verification on `:8080` demonstrates deny-before-start and allow-after-start.
