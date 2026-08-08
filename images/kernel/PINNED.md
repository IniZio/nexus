# Pinned Guest Kernel — x86_64

## Shipped artifact

| Field       | Value |
|-------------|-------|
| File        | `images/kernel/vmlinux-x86_64` |
| Architecture | x86_64 |
| Format      | uncompressed ELF vmlinux (PVH-bootable by Cloud Hypervisor) |
| Size        | ~46 MB |
| SHA-256     | `9d3570b47d5abb069ca00edfbfcef4c68306a9c3d078a01f10082b258f1001b8` |

## Provenance

Copied from `internal/core/driver/cloudhypervisor/testdata/vmlinux-x86_64`, which is the
kernel used by the passing Run 2 vsock integration tests (`internal/core/driver/cloudhypervisor`).

**Digest provenance** — SHA-256 computed locally from the committed binary:

```
sha256sum images/kernel/vmlinux-x86_64
# 9d3570b47d5abb069ca00edfbfcef4c68306a9c3d078a01f10082b258f1001b8  images/kernel/vmlinux-x86_64
```

Both copies are identical (same digest):

```
sha256sum internal/core/driver/cloudhypervisor/testdata/vmlinux-x86_64
# 9d3570b47d5abb069ca00edfbfcef4c68306a9c3d078a01f10082b258f1001b8  internal/core/driver/cloudhypervisor/testdata/vmlinux-x86_64
```

There is no upstream download URL for this binary: it originates from the local driver
integration-test fixture. The reproducible from-source build (ticket 14) will replace this
artifact with one whose upstream version tag and build inputs are fully pinned.

A second candidate kernel exists at
`.scratch/nexus3-architecture/prototypes/37-session-survives-fork/vmlinux.bin`
(proven PTY-over-vsock on Cloud Hypervisor), but `strings` reveals it lacks `virtio_pci`
and `virtio_fs` symbols built-in, making it unsuitable as the general shipped kernel.

`vmlinux-x86_64` was chosen because `strings vmlinux-x86_64 | grep -iE 'virtio_pci|virtio_fs|vsock'`
returns all three driver families:

```
virtio_fs_setup_dax
virtio_fs_request_dispatch_work
virtio_pci_find_shm_cap
virtio_pci.force_legacy
vsock_socket
virtio_vsock
vmw_vsock_virtio_transport
AF_VSOCK
```

## Required kernel config

All three driver families must be compiled **built-in** (not as modules):

| Option                    | Value | Rationale |
|---------------------------|-------|-----------|
| `CONFIG_VIRTIO_PCI`       | `y`   | PCI transport for all virtio devices under Cloud Hypervisor |
| `CONFIG_VIRTIO_FS`        | `y`   | virtiofs mount for shared workspace directories |
| `CONFIG_VIRTIO_VSOCK`     | `y`   | guest↔host vsock channel (agent comms) |
| `CONFIG_VSOCKETS`         | `y`   | AF_VSOCK socket family (prerequisite for vsock) |
| `CONFIG_MODULES`          | **not set** | No module loading; all drivers are statically linked. doc 03 decision. |

### PVH boot note

Cloud Hypervisor boots this kernel in **PVH mode** (uncompressed ELF with a PVH entry-point
header). The file is loaded directly — no bootloader, no `bzImage` wrapping. This is
consistent with how prototype 37 (`37-session-survives-fork`) validated the PTY-over-vsock
flow: Cloud Hypervisor's `--kernel` flag accepts the raw ELF.

The `init` cmdline is `init=/sbin/nexus3-agent` (agent-as-final-layer contract; ticket 14).

## Follow-up: reproducible from-source build

The current kernel is a validated binary artifact committed to the repo. A **reproducible
from-source kernel build** is a documented follow-up task (see ticket 14). That work will:

1. Pin an exact upstream Linux version tag.
2. Commit a `.config` file satisfying the table above.
3. Produce a deterministic `vmlinux` via a containerised build (e.g. `make` inside a
   pinned Debian toolchain image) and verify the SHA-256 matches.
4. Replace this binary with the reproduced artifact.

Until that follow-up lands, nexus3 reuses this validated kernel. The originals are **not
deleted** — `internal/core/driver/cloudhypervisor/testdata/vmlinux-x86_64` remains in place
so the driver integration tests continue to work without depending on the `images/` tree.

## macOS

macOS (Apple Virtualization framework) requires a different kernel compiled with
`virtio_pci` + `virtio_fs` + `vsock` built-in (no PCIe emulation layer differences apply
the same way). macOS support is **out of near-term scope** (ticket 33). A separate
`images/kernel/vmlinux-arm64` artifact will be pinned in a future slice when macOS is
targeted.
