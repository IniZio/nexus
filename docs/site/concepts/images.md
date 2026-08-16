# Images

nexus3 owns zero OCI code. An image is defined by a repo-committed `.nexus/Containerfile` and an OCI base; nexus3 builds from that using a standard BuildKit instance running inside a dedicated builder VM.

## The contract

Every project that wants a custom sandbox image commits a `.nexus/Containerfile` to its repository. The file is a standard Dockerfile/Containerfile; nexus3 imposes no custom syntax. The OCI base is declared in the FROM line.

The agent binary (`nexus3-agent`) is baked as the **final layer** of every image. This ensures the agent version matches the nexus3 host version and prevents version skew.

## Build path

```
host
│
├── nexus3 build [--file .nexus/Containerfile]
│   │
│   └── launches builder VM
│       │  same nexus3-agent binary, --builder-role flag
│       │  dedicated network namespace with unrestricted egress
│       │  cache disks mounted at designated paths
│       │
│       └── runs BuildKit (buildkitd in-guest, standard upstream)
│           │
│           └── produces OCI image layers
│               │
│               └── nexus3 wraps layers → ext4 base image
│                   stored in artifact store, content-addressed by digest
```

### Why a builder VM?

A builder VM isolates the build environment from the host and from other sandboxes. It also allows the builder to have unrestricted network egress (for pulling base images and dependencies) while workspace sandboxes run under the egress perimeter's default-deny policy.

The builder VM uses the same `nexus3-agent` binary in `--builder-role` mode. In this role the agent runs the BuildKit lifecycle and exits — it does not start the gRPC control server.

## Cache disks

The builder VM mounts dedicated **cache disks** (`--cache-disk=<device>:<mountpath>`) for the BuildKit layer cache. These are virtio-blk devices persisted across builds. A warm cache turns a minutes-long build into a seconds-long incremental build.

## The two shipped images

nexus3 ships exactly two images:

| Image | Contents |
|-------|----------|
| **Base sandbox image** | Minimal Linux userland + `nexus3-agent` as PID 1. Used by sandboxes that don't supply a `.nexus/Containerfile`. |
| **Agent rebuild image** | The toolchain needed to rebuild `nexus3-agent` itself. Used by the self-hosting build path. |

Every project that needs additional software commits a `.nexus/Containerfile` that extends the base image.

## Guest kernel

The guest kernel is shipped alongside the nexus3 binary, per-arch. It is not part of the OCI image; it is a separate artifact managed by the nexus3 host. This lets the kernel be upgraded independently of the rootfs image.

The kernel is built from source with a pinned config (`scripts/kernel/`). Key enabled features: `CONFIG_BRIDGE` + netfilter (for in-guest Docker networking), `CONFIG_VIRTIO_*`, balloon free-page reporting.

## Self-hosting base

The agent rebuild image contains the Go toolchain and the nexus3 source tree. The build path:

1. Launch a sandbox from the rebuild image.
2. Inside the sandbox, `go build ./cmd/nexus3-agent` produces a new agent binary.
3. The host harvests the binary and bakes it as the final layer of a new base image.

This path is used in CI to keep the agent binary in the shipped images up to date.

## Egress

The builder VM requires **unrestricted egress** and cannot share a workspace sandbox's default-deny posture. The driver provisions the builder VM with a separate network namespace that bypasses the egress perimeter. This is a distinct network posture from all other sandboxes.

## Content addressing

The artifact store addresses images by the SHA-256 digest of the rootfs content (`Envelope.ImageDigest`). Two sandboxes referencing the same digest share the base disk (CoW); each sandbox's writes go to its own sparse overlay.
