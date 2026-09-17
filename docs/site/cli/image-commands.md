---
title: "Image Commands"
description: "Reference for image build, ls, and prune commands"
---

# Image Commands

> Build and manage the guest images that sandboxes boot from.

Guest images are OCI-compatible root filesystem layers built by `buildkitd` inside the VM. The `image` group manages the local image store.

## nexus image build

Build a guest image from a Dockerfile context.

```
nexus image build [flags]
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--workspace <path>` | string | — | Host working tree to include in the build context (default: cwd) |
| `--ref <ref>` | string | — | Output image reference (tag) |
| `--base <ref>` | string | `debian:bookworm-slim` | Base image reference to build from |

## nexus image ls

List available guest images in the local store.

```
nexus image ls
```

## nexus image prune

Remove unused guest images and stale builder templates from the local store, and report the bytes freed.

```
nexus image prune [flags]
```

| Flag | Type | Default | Description |
|---|---|---|---|
| `--dry-run` | bool | `false` | List what would be removed without removing anything |

Two kinds of artifact are pruned:

- **Unreferenced cache entries** — any image whose digest is not referenced by a sandbox record and whose ref is not in the pinned default base set. Base images outside that set are candidates like any other entry.
- **Stale builder templates** — `nexus-builder-*.ext4` files in the image store that were produced by a previous `nexus-agent` build. The current template is identified by the tag of the `nexus-agent` binary on this host; when that binary cannot be found the template sweep is skipped and the command says so.

Templates are kept, regardless of tag, when another process holds them open (a running builder VM) or when they were modified within the last 10 minutes (a build still streaming into the file).

Dry-run output lists the candidates and the total that would be freed:

```
$ nexus image prune --dry-run
DIGEST        REF                      KIND     SIZE
e5a72e9c053e  nexus-builder:20260901  builder  1.2 GiB
FILE                                          AGENT             SIZE
nexus-builder-abc-agent0123456789abcdef.ext4  0123456789abcdef  2.0 GiB
Would free ~3.2 GiB (1 images, 1 builder templates)
```

Without `--dry-run` the command removes them and reports `Pruned 1 image(s), 1 builder template(s), freed ~3.2 GiB`. With `--json`, the envelope carries `removed`, `templates`, `freed_bytes`, `template_sweep`, and `dry_run`; a dry run also includes `images` and `builder_templates` arrays.
