# images/

This tree defines nexus3's two shipped OCI image artifacts and the pinned guest
kernel.  It contains **build definitions only** — the actual image builds happen
in CI via the builder VM (see below).

---

## Two-image model (ticket 14)

nexus3 ships **exactly two images**:

| Image | Path | Purpose |
|-------|------|---------|
| **Builder rootfs** | `images/builder/Containerfile` | A VM booted by nexus3 to run stock `buildkitd`. Drives all workspace rootfs builds. |
| **Minimal default base** | `images/base/Containerfile` | The default workspace rootfs. Carries the Go toolchain, git, ca-certs, and the nexus3 agent. |

Old nexus's `--tool` overlay set is **dropped**.  A user can supply `--image <ref>` to
override the default base; such a custom image is legal but may not support VS Code
Remote-SSH if it lacks glibc (see glibc requirement below).

---

## Builder rootfs (`images/builder/`)

Runs **stock `buildkitd`** from the upstream `moby/buildkit` image (pinned by digest).
nexus3 does **not** patch or wrap buildkitd; it communicates via BuildKit's Go client
over buildkitd's standard GRPC socket.

The builder VM is booted with **unrestricted egress** — it must pull OCI layers,
Go modules, and apt packages on behalf of user-supplied `Containerfile`s.  This is a
distinct network posture from workspace VMs, which default to deny-all egress
(ticket 14 → ticket 15).

The builder VM is **not** nexus3-agent-managed (no `init=/sbin/nexus3-agent`).

---

## Default base (`images/base/`)

### glibc requirement

The default base must use a **glibc distribution** (ticket 14, amended).  VS Code
Remote-SSH requires `glibc >= 2.28` and Microsoft explicitly does not support
Alpine / musl hosts.  The current base is `debian:bookworm-slim` (glibc 2.36).

A user-supplied `--image alpine` remains legal and will work for nexus3's core
features, but Remote-SSH will not function.

### Upstream Go toolchain

The distro Go (Debian bookworm ships 1.19) is too old for nexus3's core and agent
builds.  The base image fetches the **upstream Go tarball from go.dev** and verifies
its SHA-256 before installation (ticket 28).  No gcc or build-essential is included
— all Go code is built with `CGO_ENABLED=0`.

### Agent as the final layer

The nexus3 agent binary is baked as the **last image layer** so an agent bump
rebuilds only that layer, not the Go toolchain or apt layers (ticket 14).

### Boot contract

```
init=/sbin/nexus3-agent
```

The guest kernel's `init=` cmdline points directly at the agent.  The agent is PID 1
inside the VM.  This is a VM rootfs, not a container image; there is no container
runtime involved.

### Seeded/shared Go module cache (design note)

A self-hosting sandbox accrues ~1.8 GB of Go module+build cache and re-pays a ~32 s
cold module download on every fresh sandbox.  A future slice can bake a pre-seeded
`$GOMODCACHE` snapshot (or mount a shared read-only virtiofs volume) into the base
image to amortise this cost.  The `GOPATH` / `GOMODCACHE` environment variables are
left open for that override (ticket 28).

---

## Pinned guest kernel (`images/kernel/`)

nexus3 **ships the guest kernel per-arch** (ticket 14).  The kernel is pinned — a
reproducible from-source build is a follow-up task (see `images/kernel/PINNED.md`).

| Field       | Value |
|-------------|-------|
| File        | `images/kernel/vmlinux-x86_64` |
| Format      | Uncompressed ELF, PVH-bootable by Cloud Hypervisor |
| SHA-256     | `9d3570b47d5abb069ca00edfbfcef4c68306a9c3d078a01f10082b258f1001b8` |
| Built-in drivers | `virtio_pci`, `virtio_fs`, `virtio_vsock` / `vsockets` |
| Modules     | **none** (`CONFIG_MODULES` not set) |

The kernel is loaded by Cloud Hypervisor via its `--kernel` flag (uncompressed ELF /
PVH entry-point).  No bootloader or bzImage wrapping is used.

---

## macOS

macOS (Apple Virtualization framework) is **out of near-term scope** (ticket 33).
A `vmlinux-arm64` kernel artifact and any macOS-specific Containerfile adjustments
will be added in a future slice.

---

## Ticket references

- **Ticket 14** — image pipeline: OCI base + `.nexus/Containerfile` contract; exactly
  two images; agent-as-final-layer; guest kernel; raw ext4 artifact; builder egress.
- **Ticket 28** — self-hosting base: upstream Go tarball, no gcc, ~117 MB, seeded
  module cache design note.
- **Ticket 33** — macOS/Apple Virtualization out of scope for near-term image work.
