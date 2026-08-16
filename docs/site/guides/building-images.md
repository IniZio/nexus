# Building images

nexus3 sandboxes boot from ext4 guest images. You can use a pre-built base image or build a
custom image from a Containerfile using the in-VM buildkitd — no external Docker daemon required.

---

## Quick build with `image build`

```
nexus3 image build --workspace /path/to/project --ref my-image:latest
```

| Flag | Description |
|---|---|
| `--workspace <dir>` | Workspace root containing `.nexus/Containerfile` (default: cwd) |
| `--ref <tag>` | Human-readable tag for the resulting image |
| `--base <ref>` | OCI base image reference (default: `debian:bookworm-slim`) |

`image build` uses an in-VM buildkitd. The build runs inside a temporary sandbox so your host
environment is unaffected. When the build completes the image is stored locally and can be
referenced by tag in any subsequent `sandbox create --image` call.

List built images:

```
nexus3 image ls
```

Remove cached images not used by any live sandbox:

```
nexus3 image prune
```

---

## The Containerfile

Place your build instructions at `.nexus/Containerfile` inside the project directory.
Containerfile syntax is OCI-compatible (same as Dockerfile). Example:

```dockerfile
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y \
    git curl build-essential \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /app
```

The `--dockerfile` / `-f` flag overrides the path if you want to keep multiple configurations:

```
nexus3 sandbox create myproject/worker-1 \
  --file /path/to/project \
  --dockerfile /path/to/project/.nexus/Containerfile.dev
```

---

## Build from source with `sandbox create --file`

`sandbox create --file <dir>` is a single-step alternative that builds the image and boots the
sandbox in one command:

```
nexus3 sandbox create myproject/builder-1 \
  --file /path/to/project \
  --workspace /path/to/project \
  --memory 8192 \
  --vcpus 4
```

This is the path used by the herdr TUI integration. It:

1. Reads `.nexus/Containerfile` from `<dir>`
2. Builds the image using in-VM buildkitd
3. Boots the sandbox with the resulting image
4. Optionally captures your host working tree if `--workspace` is given

Egress (outbound network) is automatically granted for the full `--file` build path, so `apt-get`,
`pnpm install`, and image pulls all work without extra configuration.

---

## Nested KVM for faster builds

By default, `/dev/kvm` is not exposed inside the guest. For large build workloads where you want
hardware-accelerated VMs inside the sandbox, pass `--nested`:

```
nexus3 sandbox create myproject/heavy-builder \
  --file /path/to/project \
  --nested
```

`--nested` is off by default to minimise the security surface. Enable it only when the workload
specifically needs nested virtualisation.

---

## Caching layers

buildkitd maintains a layer cache on a separate virtio-blk disk inside the builder sandbox. A
warm build that hits the cache is measurably faster than a cold one. The cache is stored per
builder sandbox and is not shared between independent sandboxes.

If you want a warm cache for iterative development:

1. Build once with `--file` or `image build`.
2. For subsequent runs, use `--image <ref>` to boot directly from the cached image without
   re-running the full build.

---

## Disk sizing for builds

Large builds (compose monorepos with many services) can consume significant disk during the build
phase. Measured on a 14-service monorepo:

- Workspace capture (example-app): 6.36 GiB captured into ~12.8 GiB ext4 image (apparent ceiling)
- Warm pilot sandbox after build: ~4.57 GiB actually allocated
- buildkit cache disks are also sparse

Before starting a large build, verify available space:

```
df -h ~/.local/state/nexus3/
```

For running Docker Compose inside the sandbox (multi-service stacks), see
[Docker in a sandbox](docker-in-sandbox.md).

---

## See also

- [Surface reference — `image`](../surface.md#image--manage-guest-images)
- [Surface reference — `sandbox create`](../surface.md#sandbox--subcommands-for-the-full-sandbox-lifecycle)
- [Workspace capture](workspace-capture.md)
- [Docker in a sandbox](docker-in-sandbox.md)
