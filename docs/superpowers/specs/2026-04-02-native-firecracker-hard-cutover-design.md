# Native Firecracker Hard Cutover Design

Date: 2026-04-02
Status: Approved for planning
Scope: Repo-wide runtime cutover

## 1. Context and Decision

The current `NEXUS_RUNTIME_BACKEND=firecracker` path is not native Firecracker. It shells through LXC/LXD (`lxc launch`, `lxc exec`, socket proxy wiring), and has shown repeated runtime failures in CI and local reproductions.

Decision: perform a **hard cutover** to **native Firecracker** across the repository.

User constraints captured in this design:

- Repo-wide migration, not doctor-only.
- Hard cutover; remove old path instead of fallback or compatibility layer.
- Support all current target environments, including non-Linux host workflows through approved remote/VM setups.
- Execution/control channel is guest agent over vsock only.
- Cold-boot first; snapshot/restore can follow after cutover lands.
- Breaking configuration changes are allowed.
- Preserve strict safety rule: fail rather than running in wrong (host) context.

## 2. High-Level Architecture

Replace all LXC-backed firecracker behavior with a native runtime manager:

1. Start `firecracker` process with per-VM API socket.
2. Configure VM through Firecracker REST API:
   - `/machine-config`
   - `/boot-source`
   - `/drives/*`
   - `/vsock`
   - `/actions` (`InstanceStart`)
3. Wait for guest agent readiness over vsock.
4. Execute all doctor and runtime commands through agent RPC.
5. Stop/cleanup VM process and VM workdir explicitly.

There is no SSH control channel and no host docker socket proxy mechanism in the native path.

## 3. Components and Contracts

### 3.1 Native runtime package (new)

Create a runtime package (for example `packages/nexus/pkg/runtime/firecracker`) responsible for:

- Firecracker process lifecycle (spawn, monitor, stop, cleanup).
- Per-instance filesystem/workdir management.
- API socket readiness checks and timeout handling.
- VM boot and configuration sequencing.
- Instance handle/state for downstream execution APIs.

Primary contract (illustrative):

- `Spawn(ctx, Spec) (InstanceHandle, error)`
- `Exec(ctx, InstanceHandle, CommandSpec) (Result, error)`
- `CopyIn/CopyOut` (if needed by doctor/runtime flows)
- `Stop(ctx, InstanceHandle) error`
- `Delete(ctx, InstanceHandle) error`

### 3.2 Firecracker API client (new)

Add a dedicated Unix-socket HTTP client (pattern aligned with `forgevm/internal/providers/firecracker_api.go`).

Requirements:

- Explicit endpoint-level errors with HTTP status + response body.
- Context-aware timeouts.
- No hidden retries that mask root causes.

### 3.3 Guest agent over vsock (new Nexus-owned binary)

A minimal static Linux agent in guest image; protocol over vsock request/response.

Required v1 RPCs:

- `health`
- `exec`
- `write_file`
- `read_file`
- `mkdir`
- `stat`
- `shutdown`

Non-goals for v1:

- SSH channel
- Host-mounted control sockets
- Fallback control plane

### 3.4 Doctor integration rewrite

In `packages/nexus/cmd/nexus`, remove `firecracker` code paths that build `lxc` commands and replace with native instance + agent execution adapter.

Required behavior:

- If backend is firecracker and VM/agent is not ready, fail hard.
- Never resolve to host execution for firecracker checks.
- Surface diagnostics from native runtime and agent boundaries.

## 4. Migration Scope (Big-Bang)

### 4.1 Remove legacy firecracker-via-lxc behavior

Delete firecracker-specific LXC/LXD orchestration from doctor/runtime flow, including:

- `lxc launch/exec/config` control logic.
- Docker socket proxy device setup.
- Firecracker env knobs that only make sense for LXC transport.

### 4.2 Add native modules and tests

- Native runtime manager
- Firecracker API client
- Vsock transport + protocol handling
- Guest agent build/package integration
- Unit + integration tests for lifecycle and execution

### 4.3 Contract and documentation updates

- Introduce new native firecracker config/env contract.
- Mark old keys as removed with clear migration errors.
- Update docs in `docs/reference` and `docs/dev` for new runtime behavior.

### 4.4 Case-study updates and dogfooding

- Update `.case-studies/action-nexus` and `.case-studies/hanlun-lms` to the new contract.
- Verify `hanlun-lms` PR #216 doctor workflow passes on native firecracker.

## 5. Failure Model and Safety

No fallback policy:

- No LXC fallback
- No host-context fallback
- No SSH fallback

Failure classes:

- `bootstrap_failed`
- `vm_config_failed`
- `agent_unreachable`
- `command_failed`
- `asset_missing`

On failure, report:

- VM workdir path
- Firecracker stderr tail
- Failed API endpoint class
- Vsock/agent handshake trace summary

Cleanup:

- Best-effort stop + workdir cleanup on every exit path.
- Cleanup errors are reported but do not mask primary failure.

## 6. Verification Gates

### Gate 1: Unit and contract tests

- Firecracker API client request/response behavior.
- Runtime state transitions and timeout mapping.
- Agent protocol encode/decode and exec behavior.
- Firecracker backend wrong-context rejection.

### Gate 2: Local integration tests

- Cold boot native VM from configured kernel/rootfs.
- Agent handshake over vsock.
- Representative doctor probes/checks run fully in guest.
- No orphan firecracker processes after teardown.

### Gate 3: Repo-wide quality checks

- `go test ./...` for nexus packages
- zero type errors
- zero lint errors

### Gate 4: Case-study CI dogfood

- `action-nexus` doctor action with native contract.
- `hanlun-lms` `nexus-doctor` workflow green on firecracker backend.

### Gate 5: Negative path validation

- Missing kernel/rootfs/agent fails early with actionable error.
- Agent unavailable fails explicitly without fallback.
- Guest network/runtime failures are correctly attributed.

## 7. Breaking Changes

The cutover intentionally allows breaking external contract.

Rules:

- Old firecracker-via-lxc keys are removed.
- If removed keys are used, fail with migration guidance text.
- Migration notes are required in docs.

## 8. Deferred Work (Post-cutover)

Not part of this cutover implementation:

- Snapshot/restore optimization (cold boot only in v1 cutover)
- Performance tuning and pool prewarming
- Additional agent RPC expansion beyond v1 needs

## 9. Definition of Done

Done means all are true:

- LXC-backed firecracker path is removed from active runtime/doctor execution.
- Native Firecracker + vsock agent is the only firecracker implementation.
- Hanlun doctor workflow is green with new native path.
- Updated docs describe only implemented behavior.
- Verification evidence is captured from passing test and CI runs.
