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
3. Use different lifecycle policy by command type:
   - normal workspace start: persistent Lima instance
   - doctor: ephemeral Lima instance
4. Keep Linux Firecracker path unchanged.
5. Make `action-nexus` self-contained for Go setup so consuming workflows do not need their own setup-go step.

## Non-Goals

1. Rewriting Linux Firecracker internals.
2. Embedding complex Lima orchestration logic in `action-nexus`.
3. Guaranteeing GUI/Desktop workloads in microVMs.

## Decision Summary

1. Nexus introduces a platform adapter boundary for Firecracker runtime setup/execution.
2. Darwin adapter uses a checked-in Lima template and executes Firecracker operations in guest Linux.
3. Linux adapter keeps current native behavior.
4. `nexus doctor` on Darwin uses ephemeral Lima instances (always torn down).
5. Normal workspace start on Darwin uses persistent Lima instances (reused with health/version checks).

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

## Verification Strategy

1. Unit tests:
   - platform dispatch by host OS
   - persistent vs ephemeral instance naming and policy
   - template-version mismatch recreation decision
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

## Rollout Plan

1. Implement Nexus platform adapter and Darwin Lima path.
2. Add checked-in template and versioning metadata checks.
3. Add/adjust tests for dispatch and policy behavior.
4. Update `action-nexus` to run setup-go internally.
5. Validate Linux and macOS doctor paths in CI.

## Success Criteria

1. `action-nexus` users no longer need explicit setup-go.
2. macOS Firecracker doctor path executes under Nexus-managed Lima orchestration.
3. Workspace start uses persistent Lima, doctor uses ephemeral Lima.
4. Linux Firecracker path remains functional and CI-stable.
