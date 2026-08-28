---
title: "Building images"
description: "Build custom guest images from a Containerfile and boot a sandbox in one step"
---

# Building images

> Produce a custom ext4 guest image from a Containerfile and boot a sandbox against it — using the in-VM buildkitd, no external Docker daemon required.

nexus3 sandboxes boot from ext4 guest images. You have two paths: run a stock OCI image directly (no build step), or build a custom image from a Containerfile.

---

## Run a stock OCI image directly <Badge type="tip" text="built" />

For unmodified Docker Hub / OCI images, no build step is needed. Pass the registry ref directly to `nexus3 run` or `nexus3 create --image`:

```sh
nexus3 run alpine:3.20 -- sh -c 'echo hello; cat /etc/os-release | head -1'
nexus3 run debian:bookworm-slim -- cat /etc/debian_version
nexus3 run python:3.12 -- python3 -c 'import sys; print(sys.version)'
```

nexus3 checks its local image store first. On a cache miss the image is pulled from the registry, converted to a bootable ext4 rootfs, and cached by ref. Subsequent runs of the same ref skip the pull.

> **Egress requirement:** the initial pull needs outbound HTTPS to the registry (e.g. `registry-1.docker.io`). Ensure the host's egress policy allows the registry host, or add it to `egress.policy` in `nexus3.yaml`.

Use this path when:
- You want to try a stock runtime quickly.
- The image needs no customisation.
- You do not want to manage a Containerfile.

For iterative development or custom tooling, build a custom image (below) so you control the dependency set and avoid repeated network pulls.

---

## Build a custom image from a Containerfile

**1. Write a Containerfile**

Place build instructions at `.nexus/Containerfile` inside the project directory. Containerfile
syntax is OCI-compatible (same as Dockerfile):

```dockerfile
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y \
    git curl build-essential \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /app
```

Use `--dockerfile` / `-f` to override the path when keeping multiple configurations:

```sh
nexus3 create myproject/worker-1 \
  --context /path/to/project \
  --dockerfile /path/to/project/.nexus/Containerfile.dev
```

<Badge type="warning" text="partial" /> — current implementation uses `nexus3 sandbox create`; see [CLI sandbox commands](/cli/sandbox-commands) for the mapping.

<Badge type="warning" text="partial" /> — current implementation uses `--file`; see [CLI sandbox commands](/cli/sandbox-commands) for the mapping.

**2. Build and boot in one step**

`nexus3 create --context <dir>` builds the image and boots the sandbox in a single command:

```sh
nexus3 create myproject/builder-1 \
  --context /path/to/project \
  --mount /path/to/project:/workspace/project \
  --memory 8192 \
  --vcpus 4
```


Steps performed:
1. Reads `.nexus/Containerfile` from `<dir>`
2. Builds the image using in-VM buildkitd
3. Boots the sandbox with the resulting image
4. Mounts the source directory as a live virtiofs share if `--mount` is given

Egress is automatically granted for the full `--context` build path, so `apt-get`, `pnpm install`,
and image pulls all work without extra configuration.

**3. Or build the image separately, then boot**

```sh
nexus3 image build --workspace /path/to/project --ref my-image:latest
```

| Flag | Description |
|---|---|
| `--workspace <dir>` | Workspace root containing `.nexus/Containerfile` (default: cwd) |
| `--ref <tag>` | Human-readable tag for the resulting image |
| `--base <ref>` | OCI base image reference (default: `debian:bookworm-slim`) |

Boot from the cached image without rebuilding:

```sh
nexus3 create myproject/worker-1 --image my-image:latest
```

List and prune built images:

```sh
nexus3 image ls
nexus3 image prune
```

---

## Nested KVM for faster builds

For large build workloads that need hardware-accelerated VMs inside the sandbox, pass `--nested`:

```sh
nexus3 create myproject/heavy-builder \
  --context /path/to/project \
  --nested
```

<Badge type="warning" text="partial" /> — current implementation uses `nexus3 sandbox create` and `--file`; see [CLI sandbox commands](/cli/sandbox-commands) for the mapping.

`--nested` is off by default to minimise the security surface. Enable it only when the workload
specifically needs nested virtualisation.

---

## Caching layers

buildkitd maintains a layer cache on a separate virtio-blk disk inside the builder sandbox. The
cache is stored per builder sandbox and is not shared between independent sandboxes.

For iterative development:

1. Build once with `nexus3 create --context` or `image build`.
2. For subsequent runs, use `--image <ref>` to boot directly from the cached image without
   re-running the full build.

---

## Declaring startup services <Badge type="danger" text="not built" />

Services baked into the image start automatically and are readiness-gated: `nexus3 create`
returns only once every declared ready probe passes (30-second cap; `create` fails if the cap
is exceeded).

Declare services by placing a `services.yaml` file in `.nexus/` and copying it into the image:

```yaml
# .nexus/services.yaml → baked to /etc/nexus3/services.yaml
services:
  - name: dockerd
    command: [dockerd, --storage-driver=overlay2]
    ready: [docker, info]
    restart: never
```

```dockerfile
# .nexus/Containerfile
FROM debian:bookworm-slim

# ... install packages ...

COPY .nexus/services.yaml /etc/nexus3/services.yaml
```

With this in place, `nexus3 create --context .` blocks until `docker info` exits zero. The next
command can use `docker` with no poll loop. See [Docker in a sandbox](docker-in-sandbox.md) for a
full worked example.

Each entry supports:

| Field | Description |
|---|---|
| `name` | Unique identifier for the service |
| `command` | Command and arguments as a YAML sequence |
| `ready` | Readiness probe command; polled until it exits 0 |
| `restart` | `never` (default) — the agent does not restart crashed services |

A `--service 'name:cmd[:readyprobe]'` flag on `nexus3 create` can add or override a same-named
entry at create time, without rebuilding the image.

---

## Disk sizing for builds

Large builds can consume significant disk during the build phase. Before starting a large build,
verify available space:

```sh
df -h ~/.local/state/nexus3/
```

Measured on a 14-service compose monorepo:

| Metric | Value |
|---|---|
| Source workspace apparent ceiling | ~12.8 GiB ext4 image (6.36 GiB source) |
| Warm pilot sandbox after build — allocated | ~4.57 GiB |

buildkit cache disks are also sparse and are included in the total.

For running Docker Compose inside the sandbox, see [Docker in a sandbox](docker-in-sandbox.md).

---

## See also

- [Surface reference — `image`](/cli/#commands)
- [Surface reference — `nexus3 create`](/cli/#commands)
- [Mounts and worktrees](mounts-and-worktrees.md)
- [Docker in a sandbox](docker-in-sandbox.md)
