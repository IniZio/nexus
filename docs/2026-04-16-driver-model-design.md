# Workspace Driver Model

**Date:** 2026-04-16  
**Status:** Approved design — pending implementation  
**Scope:** `packages/nexus` runtime driver taxonomy, selection, mounting, forking, and integration test contracts

---

## Overview

The Nexus daemon supports multiple execution backends ("drivers") for running workspaces. This document defines the canonical driver taxonomy, how drivers are selected, how workspace paths are mounted, how forking works per driver, and the integration test matrix that all implementations must satisfy.

The current codebase has a leaked abstraction: the `firecracker` backend name is overloaded across three distinct execution models (native KVM, Lima+Firecracker, Lima pool). The capability/preflight model adds indirection without value. This document defines the target model that the refactor will implement.

---

## 1. Driver Taxonomy

Three canonical driver names:

| Driver | Platform | Isolation levels |
|--------|----------|-----------------|
| `firecracker` | Linux | `vm/dedicated`, `vm/pool` |
| `lima` | macOS | `vm/dedicated`, `vm/pool` |
| `sandbox` | Linux + macOS | `process` |

### `firecracker`

Native Firecracker microVM on Linux. Requires KVM (`/dev/kvm`).

- **`vm/dedicated`** — one VM per workspace. `ProjectRoot` mounted directly to `/workspace` via virtio block or virtiofs. Fork uses btrfs subvolume snapshot (O(1) CoW).
- **`vm/pool`** — all workspaces share a single long-running Firecracker VM. Each workspace has a btrfs subvolume at `/workspace/<id>` in the shared VM; the process sees `/workspace` via per-process mount namespace (see Section 2). Fork uses btrfs subvolume snapshot within the shared VM.

### `lima`

Lima VM proxy on macOS. The Lima VM is the execution environment; the daemon communicates via SSH.

- **`lima/dedicated`** — macOS with nested virtualization (`kern.hv_support=1`) and `vm.mode=dedicated`. A per-workspace Lima VM is spun up; inside that Lima VM, Firecracker runs as the microVM layer. The workspace sees a full nested VM stack: macOS host → Lima VM → Firecracker microVM. `ProjectRoot` mounted at `/workspace`. Fork uses btrfs subvolume snapshot.
- **`lima/pool`** — macOS without nested virtualization, or `vm.mode=pool`. All workspaces share the single `nexus` Lima instance. Each workspace has a btrfs subvolume at `/workspace/<id>`; the process sees `/workspace` via per-process mount namespace. Fork uses btrfs subvolume snapshot within the shared VM.

### `sandbox`

Process sandbox. No VM. Runs directly on the host.

- macOS: `sandbox-exec` (seatbelt). The workspace `ProjectRoot` is accessed at its host path; `sandbox-exec` restricts syscalls but does not remap paths. The process sees `/workspace` via a symlink or profile-level path binding (not bwrap).
- Linux: `bwrap` (bubblewrap), `--bind <ProjectRoot> /workspace`. The process sees `/workspace` as the canonical path.
- Fork on macOS: `clonefile()` via `copyfile(3)` with `COPYFILE_CLONE` — instant CoW on APFS. Falls back to hardlink tree if not on an APFS volume.
- Fork on Linux: hardlink tree (`cp -al`) — O(file count), no data copied. Files diverge on write via create+rename (standard tool pattern). Not safe for in-place binary mutators; acceptable for dev tooling workloads.

---

## 2. Workspace Path Mounting

**The canonical workspace path inside any running workspace is `/workspace`.** This holds for all drivers and all modes. Processes never see a namespaced path.

### How each driver achieves `/workspace`

**`firecracker/dedicated`, `lima/dedicated`**  
`ProjectRoot` is mounted directly to `/workspace` in a per-workspace VM. No indirection needed.

**`firecracker/pool`, `lima/pool`**  
The shared VM has workspace data volumes mounted at `/workspace/<id>` (btrfs subvolumes) to avoid collisions. When a workspace shell or agent session is spawned, the remote command is wrapped with a per-process mount namespace:

```sh
unshare --mount -- sh -c 'mount --bind /workspace/<id> /workspace && cd /workspace && exec <shell> -i'
```

Inside this namespace, `getcwd()` returns `/workspace/...`. This is correct Linux VFS behavior: `getcwd(2)` walks `/proc/self/mountinfo` entries for the process's private namespace, where `/workspace` is the terminal mount point. The namespaced path `/workspace/<id>` is only visible in the shared (parent) namespace used by workspace management operations.

`unshare --mount` works unprivileged on Ubuntu 22+ (Lima default) when user namespaces are enabled (`/proc/sys/user/max_user_namespaces > 0`). The existing passwordless `sudo -n mount --bind` is retained for the initial `/workspace/<id>` subvolume mount; the per-process `unshare` step is a second layer.

**`sandbox`**  
`bwrap --bind <ProjectRoot> /workspace` (Linux) or equivalent seatbelt profile (macOS). Process always sees `/workspace`.

### Mounting constraints

**UID mismatch on `lima`**  
The guest `lima` user (UID 1000) does not match the macOS host user UID (typically 501). Standard bind mounts do not remap UIDs. The workaround — `chmod a+r` on `.git` metadata and objects after mount — is an inherent constraint of bind mounts without `idmapped` mount support in Lima's VZ backend. It is not a fixable implementation mistake. The `gitBindMountPermissionFixScript` in `guest_driver.go` is correct and must be retained.

**btrfs data volume requirement**  
Pool mode (both `firecracker` and `lima`) requires the workspace data volume inside the VM to be formatted as btrfs. This is a provisioning requirement. The rootfs image format is unaffected. See Section 4 (Fork) for the PoC requirement.

**Teardown operates in shared namespace**  
`teardownWorkspacePath` unmounts `/workspace/<id>` in the shared (parent) namespace. This is correct — teardown must not run inside a per-process namespace. Only shell/agent spawn sessions use the private namespace.

### Known implementation smell

The current code passes `/workspace/<id>` as the working directory to SSH (`guestWorkdirForID`), so workspace processes currently see the namespaced path. This is the bug the per-process mount namespace approach fixes. No workspace management code (setup, teardown, bootstrap scripts) is affected — they operate on `/workspace/<id>` in the shared namespace as before.

---

## 3. Driver Selection

Driver selection is deterministic from **platform** and **config**. No runtime capability probing. The capability/preflight model (`RunFirecrackerPreflight`, installable/missing states) is removed.

### Selection decision tree

```
platform = linux
  isolation.level = "vm"
    vm.mode = "dedicated"  →  firecracker/dedicated
    vm.mode = "pool"       →  firecracker/pool
  isolation.level = "process"  →  sandbox

platform = darwin
  isolation.level = "vm"
    kern.hv_support = 1 (nested virt available)
      vm.mode = "dedicated"  →  lima/dedicated
      vm.mode = "pool"       →  lima/pool
    kern.hv_support = 0 (nested virt unavailable)
      (forced)               →  lima/pool
  isolation.level = "process"  →  sandbox
```

### Key points

- `kern.hv_support` is read once at daemon startup as a hardware capability fact. It is not a preflight probe. If nested virt is unavailable and `vm/dedicated` is configured, the daemon logs a warning and falls back to `lima/pool` — it does not fail.
- If a required binary (e.g. `firecracker`) is absent, the daemon returns a clear error at workspace create time. There is no install-on-demand flow.
- `vm.mode` defaults: `pool` on macOS, `dedicated` on Linux.
- `lima` and `firecracker` are distinct driver identities. The `lima` driver does not claim the `firecracker` backend name.

### Known implementation smell

`selection/service.go` currently contains an explicit `runtimeSetupGOOS == "darwin"` check that routes Darwin to a driver claiming the `firecracker` backend name. This is the core leaked abstraction. In the target model this is replaced by the clean tree above, and the `lima` driver registers as `lima`.

The capability/preflight model in `selection/service.go` (preflight pass/fail/installable states) is removed entirely. Selection is config-driven, not probe-driven.

---

## 4. Fork Behavioral Contracts

Fork creates an independent child workspace whose state starts identical to the parent at fork time. The parent continues operating normally. Parent and child diverge independently from the fork point.

### Per-driver fork mechanism

| Driver | Mechanism | Speed | Requirement |
|--------|-----------|-------|-------------|
| `firecracker/dedicated` | btrfs subvolume snapshot | O(1), CoW | btrfs guest data volume |
| `firecracker/pool` | btrfs subvolume snapshot (within shared VM) | O(1), CoW | btrfs guest data volume |
| `lima/dedicated` | btrfs subvolume snapshot | O(1), CoW | btrfs guest data volume |
| `lima/pool` | btrfs subvolume snapshot (within shared VM) | O(1), CoW | btrfs guest data volume |
| `sandbox` (macOS) | `clonefile()` via `copyfile(3) COPYFILE_CLONE` | O(1), CoW | APFS host volume (falls back to hardlink tree on non-APFS) |
| `sandbox` (Linux) | hardlink tree (`cp -al`) | O(file count) | Any POSIX FS |

### btrfs fork operation

```sh
# On fork (inside shared VM or dedicated VM):
btrfs subvolume snapshot /workspace/<parent-id> /workspace/<child-id>
```

The snapshot is read-write. Both parent and child are independent btrfs subvolumes from fork time. CoW is handled at the btrfs layer — no data is copied until written.

### Required PoC before implementation

btrfs snapshot is the correct mechanism but requires validation:

1. Firecracker's kernel config must include `CONFIG_BTRFS_FS`. The default Firecracker kernel (Kata-derived) may not include it — a custom kernel profile may be required (as used by the `fcvm` project).
2. Lima's Ubuntu guest image must support provisioning a separate btrfs data volume for `/workspace`.

This PoC must pass before the fork implementation is written.

### Known bad implementation

The current `lima` pool fork uses `copyWorkspaceTree` into `lineage-snapshots/` — a recursive file copy that is O(data size) and blocks the fork operation. This is the wrong primitive and is replaced by btrfs snapshot in the target model.

---

## 5. Integration Test Matrix

Every driver × every user action has a test. Tests assert behavioral contracts. If the contract holds, the test passes regardless of implementation detail. Tests that require unavailable hardware are skipped with explicit `t.Skip("requires KVM")` — never silently omitted.

### Test matrix

| Action | `firecracker/dedicated` | `firecracker/pool` | `lima/dedicated` | `lima/pool` | `sandbox` |
|--------|:-:|:-:|:-:|:-:|:-:|
| Create workspace | ✓ | ✓ | ✓ | ✓ | ✓ |
| Workspace path is `/workspace` | ✓ | ✓ | ✓ | ✓ | ✓ |
| `getcwd()` returns `/workspace` | ✓ | ✓ | ✓ | ✓ | ✓ |
| Write file, read back | ✓ | ✓ | ✓ | ✓ | ✓ |
| Fork — child starts at parent state | ✓ | ✓ | ✓ | ✓ | ✓ |
| Fork — parent diverges independently | ✓ | ✓ | ✓ | ✓ | ✓ |
| Fork — child diverges independently | ✓ | ✓ | ✓ | ✓ | ✓ |
| Fork — completes in < 2s | ✓ | ✓ | ✓ | ✓ | macOS only |
| Two workspaces coexist (pool: same VM) | — | ✓ | — | ✓ | ✓ |
| Destroy — mounts cleaned up | ✓ | ✓ | ✓ | ✓ | ✓ |
| Destroy — sibling workspace unaffected | — | ✓ | — | ✓ | ✓ |
| `git status` exits 0 inside workspace | ✓ | ✓ | ✓ | ✓ | ✓ |
| Exec command — exit code propagated | ✓ | ✓ | ✓ | ✓ | ✓ |

### Behavioral contracts

**Create**
- After create, workspace shell exec returns exit 0 for `true`

**Path normalization**
- `pwd` inside workspace shell returns `/workspace`
- `realpath /workspace` returns `/workspace` (not a namespaced path)

**Write/read**
- File written in one shell session is readable in a subsequent session in the same workspace

**Fork**
- File written to parent before fork is visible in child immediately after fork
- File written to parent after fork is NOT visible in child
- File written to child after fork is NOT visible in parent
- Wall clock time from fork call to fork completion < 2s regardless of workspace directory size (asserted for all VM-backed drivers and macOS sandbox; explicitly skipped for Linux sandbox hardlink fallback)

**Coexistence (pool modes)**
- Two workspaces in pool mode both report `/workspace` as cwd
- Write to workspace A is not visible in workspace B
- Destroy workspace A does not affect workspace B

**Destroy**
- After destroy, workspace is unreachable
- In pool mode: guest btrfs subvolume at `/workspace/<id>` is deleted and unmounted

**git**
- `git status` exits 0 inside workspace (validates UID/permission workaround is effective for `lima` drivers; validates bind mount setup for all drivers)

### CI environment requirements

| Driver | CI environment | Condition |
|--------|---------------|-----------|
| `sandbox` | Any Linux runner | Always runs |
| `firecracker/dedicated` | Linux runner with KVM | `runs-on: ubuntu-latest` with KVM device, or self-hosted |
| `firecracker/pool` | Linux runner with KVM | Same as above |
| `lima/dedicated` | macOS runner, M-series | `macos-latest` (Apple Silicon, `kern.hv_support=1`) |
| `lima/pool` | Any macOS runner | `macos-latest` or `macos-13` |

The GitHub Actions matrix must explicitly enumerate all five driver configurations. Each run reports pass, skip (with reason), or fail — no driver is silently excluded.

---

## Appendix: Things Explicitly Out of Scope

- Package/file structure changes (structural debt is tracked separately; this doc defines behavior only)
- Auth bundle delivery and RPC surface (unrelated to driver model)
- Workspace manager lifecycle beyond create/fork/destroy
