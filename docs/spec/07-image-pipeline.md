# 07 — Image Pipeline

*Purpose: how a rootfs is built — the OCI base + `.nexus/Containerfile` contract, the one builder-VM path over stock BuildKit, the two shipped images, and the guest kernel. nexus3 owns zero OCI code.*

## The contract: OCI base + repo-committed `.nexus/Containerfile`

An image is defined by an **OCI base + a repo-committed `.nexus/Containerfile`** (old nexus's model, kept by user ruling; ticket 14). The project declares its base and build steps in-repo; nexus3 builds from that.

## One rootfs path: the builder VM over stock BuildKit

There is **one rootfs path for everything** (ticket 14): a **builder VM running stock `buildkitd`, driven by BuildKit's Go client**. Even a bare `--image alpine` goes through the builder — so **nexus3 owns zero OCI code**. This deletes old nexus's `ocitools` and hand-rolled image-baking (~11.8k LOC; map Correction) and the crane dependency.

- `internal/core/builder` is a proper core module that **consumes `driver`** to run the builder VM (ticket 21).
- macOS decided the builder: a Linux image needs a Linux kernel, so a Mac needs a VM regardless, and nexus3 already owns one on both platforms.
- The builder exports **one raw ext4 artifact** for both platforms.

## Agent baked as the final layer

The guest agent is **baked as the final layer** (`init=/sbin/nexus3-agent`), so an agent bump rebuilds **only that layer** (ticket 14).

## Guest kernel shipped per-arch

nexus3 **ships the guest kernel per-arch**: pinned config, **no `CONFIG_MODULES`** (doc 03). The Linux/CH kernel carries a PVH header. (The macOS/VZ kernel needs PCIe-virtio — `virtio_pci`/`virtio_fs`/`vsock` built in — a macOS-only constraint in doc 12; ticket 33.)

## Exactly two images

nexus3 ships **exactly two images** (ticket 14):

1. the **builder rootfs** (stock buildkitd); and
2. **one minimal default base**.

No curated toolchain set; old nexus's `--tool` overlays are **dropped**.

- **The default base must be glibc** (ticket 14, amended): VS Code Remote-SSH requires `glibc ≥ 2.28` and Microsoft states Alpine / non-glibc SSH hosts are unsupported. A user-supplied `--image alpine` stays legal and simply does not support Remote-SSH.
- The base must also carry **perimeter plumbing** (ticket 15, seed/config not baked into these decisions): the per-sandbox MITM **CA seeded into the guest trust store** at start (so goproxy's TLS interception is trusted) and **SSH client config** (`SSH_AUTH_SOCK` wiring + `known_hosts` for allowlisted git hosts). Detail in doc 08.

## Self-hosting base

The self-hosting workspace base (ticket 28) = **upstream Go tarball** (distro Go is too old — bookworm ships 1.19) + **git** + **ca-certificates**, **no gcc** (core + agent build `CGO_ENABLED=0`). ~117 MB.

**Design note (not a v1 decision):** a self-hosting workspace accrues ~1.8 GB module+build cache per sandbox and re-pays the ~32s cold module download on a fresh sandbox, so a **seeded/shared Go module cache** is worth considering.

**GOFLAGS risk:** the in-workspace `go build ./...` runs with `GOFLAGS=-mod=mod` so that the seeded module cache can be resolved offline without a pre-fetched `go.sum`. This flag permits `go` to update `go.mod` and `go.sum` inside the guest — a mutation risk if a dep or Go version changes. Watch this when bumping the Go toolchain or adding dependencies; regenerate the seeded cache and re-pin the image.

## Cache

Cache is **global and digest-pinned**, with explicit **`nexus3 image prune`** and **no automatic eviction** (ticket 14).

## Egress caveat: the builder VM needs unrestricted egress

The **builder VM needs unrestricted egress** and **cannot share a workspace's default-deny posture** (ticket 14 → 15). This is a distinct network posture for the builder, called out to doc 08.

---

*Sources: tickets 14 (OCI base + Containerfile, one builder path, zero OCI code, two images, agent-as-layer, guest kernel, raw ext4, cache/prune, builder egress), 21 (builder consumes driver), 28 (self-host base, ~117MB, module cache note), 15 (CA + SSH config seed). Map Corrections: ~11.8k LOC image-baking figure, glibc default base.*
