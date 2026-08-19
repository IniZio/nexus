---
title: "Configuration"
description: "Self-contained image build with --context and --dockerfile"
---

# Configuration

> Build a guest image from a Dockerfile at create time, without a separate build step.

The `--context <dir>` flag on `create` enables a self-contained build mode: nexus3 starts a buildkitd inside the VM, builds the image from the supplied Dockerfile context, and then boots the sandbox from the resulting image. No pre-built image reference is needed.

::: warning Implementation spelling <Badge type="warning" text="partial" />
The flag is built today as `--file`. The target spelling is `--context`. All examples on this page use the target spelling.
:::

## Self-contained build flags

These flags are part of `create`. See [Lifecycle commands](/cli/sandbox-commands) for the full flag set.

| Flag | Type | Default | Description |
|---|---|---|---|
| `--context <dir>` | string | — | Dockerfile build context directory on the host |
| `--dockerfile <path>` | string | `Dockerfile` | Dockerfile path, relative to `--context` |

`--context` and `--image` / `--rootfs` are mutually exclusive: exactly one image source must be provided.

## Example

```
nexus3 create myproject/dev-1 \
  --context /data/repos/myrepo \
  --memory 8192
```

<Badge type="warning" text="partial" /> — current implementation uses `nexus3 sandbox create` and `--file`; see [CLI sandbox commands](/cli/sandbox-commands) for the mapping.

nexus3 copies the context directory into the VM, runs `buildkitd`, builds the image, and boots the sandbox. The build cache is stored on a virtio-blk disk and reused across subsequent `--context` creates for the same project.

## What `--context` captures

The context directory is captured at create time. The capture includes:

- Tracked files (including dirty/modified tracked files)
- Untracked files
- Unpushed commits

It does not include `.git` history beyond what the working tree reflects.

## No separate config file

There is no `nexus3.yaml` or equivalent project configuration file today. All configuration is expressed as flags on the command that creates the sandbox. If a declarative project config file is added in the future, it will be documented here.
