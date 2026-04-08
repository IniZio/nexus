# macOS Firecracker via Nexus-Owned Lima (Platform Ownership)

Date: 2026-04-05
Status: Proposed (user-reviewed sections approved in chat)

## Context

We want `nexus` to support Firecracker-oriented workflows on macOS runners while keeping a clear ownership boundary:

- `nexus` owns platform-specific runtime setup and orchestration.
- `action-nexus` remains a thin wrapper that prepares tooling and invokes `nexus doctor`.

We also need to keep Linux Firecracker behavior stable and preserve CI reliability.

## Goals

1. Keep platform logic in Nexus, not in `action-nexus`.
2. Use checked-in, versioned Lima template(s) in Nexus for Darwin Firecracker path.
3. Make `nexus init` the single idempotent entrypoint that handles both scaffolding and platform readiness checks/setup.
4. Use different lifecycle policy by command type:
   - normal workspace start: persistent Lima instance
   - doctor: ephemeral Lima instance
5. Keep Linux Firecracker path unchanged.
6. Make `action-nexus` self-contained for Go setup so consuming workflows do not need their own setup-go step.

## Non-Goals

1. Rewriting Linux Firecracker internals.
2. Embedding complex Lima orchestration logic in `action-nexus`.
3. Guaranteeing GUI/Desktop workloads in microVMs.

## Decision Summary

1. Nexus introduces a platform adapter boundary for Firecracker runtime setup/execution.
2. Darwin adapter uses a checked-in Lima template and executes Firecracker operations in guest Linux.
3. Linux adapter keeps current native behavior.
4. `nexus init` performs scaffold + runtime readiness, fails hard when platform setup is incomplete, and prints manual next-step instructions.
5. Re-running `nexus init` after manual steps re-validates readiness and only succeeds when environment/tooling are actually ready.
6. `nexus doctor` on Darwin uses ephemeral Lima instances (always torn down).
7. Normal workspace start on Darwin uses persistent Lima instances (reused with health/version checks).

## Architecture

### Firecracker Platform Adapter

Define an internal interface in Nexus for platform-specific orchestration, selected at runtime by host OS:

- Linux adapter:
  - existing native setup (`setup firecracker`, bridge/tap helper, host firecracker)
- Darwin adapter:
  - Lima-backed flow (`limactl`) to run Linux-side setup and Firecracker lifecycle commands

This keeps runtime decision-making in one place and prevents action-layer drift.

### Checked-In Lima Template

Add versioned template file(s) in Nexus source (for example under `packages/nexus/templates/lima/`).

Template responsibilities:

- nested virtualization enabled
- required mounts for workspace/tooling handoff
- baseline guest provisioning needed by Nexus Firecracker flow

Nexus will carry a template version marker and compare it against instance metadata. Mismatch triggers recreation for persistent instances.

## Control Flow

### `nexus init` (single entrypoint)

1. Scaffold `.nexus/*` metadata idempotently.
2. Resolve runtime backend from workspace config/default.
3. Run platform readiness/setup through adapter:
   - Linux firecracker: host setup/readiness checks
   - Darwin firecracker: limactl install/setup/readiness checks + template checks
4. If privileged or manual steps are required, fail hard and print exact commands.
5. On re-run, verify those steps succeeded before returning success.

### Non-interactive / Read-only terminal safety

`nexus init` must never hang waiting for privilege prompts in non-interactive contexts
(for example OpenCode/CI/read-only terminals):

1. Detect non-interactive terminal/session up front.
2. Detect unavailable passwordless sudo (`sudo -n`).
3. Fail fast with explicit status and manual next-step commands.
4. Never block on interactive password prompts.
5. Re-running after manual completion performs the same readiness verification and only succeeds when prerequisites are truly satisfied.

`nexus setup firecracker` is removed/deprecated in favor of this init path.
### A) Normal Workspace Start (Darwin + Firecracker)

1. Resolve persistent instance name (stable per expected scope).
2. Acquire local lock.
3. Validate instance health and template version.
4. If missing/unhealthy/version-mismatch: recreate once.
5. Execute Firecracker setup/start path inside Lima guest.
6. Stream logs back through Nexus.

### B) Doctor (Darwin + Firecracker)

1. Generate unique ephemeral instance name (run-scoped).
2. Create and initialize Lima instance.
3. Execute doctor-related Firecracker checks and runtime path inside guest.
4. Always teardown instance in `defer`/finally style.
5. If teardown fails, report warning with diagnostics.

### C) Linux + Firecracker

No behavior change intended beyond portability fixes needed to compile non-Linux targets.

## Error Handling

Nexus returns explicit errors for:

- `limactl` missing on Darwin
- Lima startup failure
- nested virtualization unavailable/disabled
- guest bootstrap failure
- Firecracker setup failure in guest
- required privileged/manual setup not yet completed during `nexus init`
- non-interactive/read-only terminal where privileged setup cannot proceed automatically

`nexus init` behavior:

- fail hard when environment is not fully ready
- print actionable manual next-step instructions
- re-check readiness on every rerun and only pass once setup is truly complete
- in non-interactive terminals, fail immediately (no prompt hangs) when passwordless privilege escalation is unavailable

Persistent mode recovery:

- attempt auto-recreate once on health/version failure
- fail with actionable diagnostics if retry fails

Doctor mode recovery:

- fail fast on setup/runtime errors
- still run teardown best-effort

## Action Boundary (`action-nexus`)

`action-nexus` remains thin:

- add `actions/setup-go` in composite action
- expose optional `go-version` input (default `1.24.x`)
- continue building/running Nexus doctor
- do not add Lima orchestration logic

Result: downstream workflows can omit manual setup-go, and platform orchestration remains Nexus-owned.

The action invokes `nexus init` (idempotent) rather than relying on a separate setup command.
## Verification Strategy

1. Unit tests:
   - platform dispatch by host OS
   - persistent vs ephemeral instance naming and policy
   - template-version mismatch recreation decision
   - non-interactive privilege path fails fast without prompt attempts
   - manual-instruction output is emitted for read-only/non-interactive sessions
2. Build/compile checks:
   - Darwin compile succeeds (no unconditional references to Linux-only symbols)
   - Linux Firecracker tests remain green
3. CI behavior:
   - `action-nexus` works without caller-managed setup-go
   - macOS doctor path delegates platform work to Nexus logic

## Risks and Mitigations

1. Drift in persistent Lima instances
   - Mitigation: template version stamping and auto-recreate on mismatch
2. Concurrency collisions on shared runners
   - Mitigation: lock + deterministic naming for persistent mode; unique naming for doctor
3. Cleanup leaks in ephemeral doctor mode
   - Mitigation: mandatory teardown with warning path and diagnostics capture
4. Stuck CI/terminal sessions due to interactive sudo prompts
   - Mitigation: mandatory `sudo -n` gating in non-interactive sessions and immediate manual-instruction failure path

## Rollout Plan

1. Move Firecracker setup/readiness orchestration into `nexus init`.
2. Implement Nexus platform adapter and Darwin Lima path.
3. Add checked-in template and versioning metadata checks.
4. Add/adjust tests for init idempotency, hard-fail semantics, and dispatch behavior.
5. Deprecate/remove `nexus setup firecracker` command surface.
6. Update `action-nexus` to run setup-go internally and rely on init path.
7. Validate Linux and macOS doctor paths in CI.

## Success Criteria

1. `action-nexus` users no longer need explicit setup-go.
2. `nexus init` is idempotent and is the only required setup command.
3. `nexus init` fails hard when not ready, with clear manual next steps.
4. Re-running `nexus init` after manual steps verifies readiness and succeeds only when ready.
5. macOS Firecracker doctor path executes under Nexus-managed Lima orchestration.
6. Workspace start uses persistent Lima, doctor uses ephemeral Lima.
7. Linux Firecracker path remains functional and CI-stable.
8. Non-interactive/read-only terminals never hang on privilege prompts; they fail fast with manual steps.

## Doctor Startup Acceptance Criteria (Strict)

1. If project `Makefile` has a `start:` target, doctor startup must select and run `make start`.
2. In that case, doctor logs must include an explicit selection line:
   - `doctor lifecycle start selected command: make start`
3. In that case, doctor must not invoke legacy lifecycle setup/start scripts:
   - no `bash .nexus/lifecycles/setup.sh`
   - no `bash .nexus/lifecycles/start.sh`
4. If no Makefile `start` target exists, doctor may fall back to compose/lifecycle behavior per existing policy.
5. Startup command output must remain streamed to CI logs (no hidden buffering of the selected startup path).

## Compose Port Discovery Acceptance Criteria (Strict)

1. Compose published-port discovery must parse only stdout JSON from `docker compose ... config --format json`.
2. Stderr or non-JSON output must never be fed into JSON parsing.
3. If stdout is non-JSON (or `--format json` unsupported), doctor must emit a clear compose-discovery warning and continue probe/test execution.
4. The warning must be actionable and must not contain misleading JSON parse errors caused by mixed stdout/stderr streams.
