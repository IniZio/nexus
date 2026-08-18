---
title: "Image Commands"
description: "Reference for image build, ls, and prune commands"
---

# Image Commands

> Build and manage the guest images that sandboxes boot from.

Guest images are OCI-compatible root filesystem layers built by `buildkitd` inside the VM. The `image` group manages the local image store.

## nexus3 image build

Build a guest image from a Dockerfile context.

```
nexus3 image build [flags]
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--workspace <path>` | string | — | Host working tree to include in the build context (default: cwd) |
| `--ref <ref>` | string | — | Output image reference (tag) |
| `--base <ref>` | string | `debian:bookworm-slim` | Base image reference to build from |

## nexus3 image ls

List available guest images in the local store.

```
nexus3 image ls
```

## nexus3 image prune

Remove unused guest images from the local store.

```
nexus3 image prune
```
