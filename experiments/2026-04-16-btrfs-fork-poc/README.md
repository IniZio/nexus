# btrfs Fork PoC Results

## Firecracker kernel btrfs support

- Kernel version: 5.10.239 (Firecracker CI upstream, `vmlinux-5.10.239`)
- Kernel source URL: `https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.13/x86_64/vmlinux-5.10.239`
- CONFIG_BTRFS_FS: **not present**
- Evidence: zero btrfs-related strings in the kernel binary (`strings vmlinux-5.10.239 | grep -i btrfs` returns nothing); no embedded `IKCFG_ST` config block that can be decoded
- Custom kernel required: **yes**

The Firecracker CI kernel is intentionally minimal. btrfs is not compiled in. To use btrfs-based fork inside Firecracker guests the project must build and ship a custom kernel with `CONFIG_BTRFS_FS=y`. This is a non-trivial but one-time build task (see `docs/roadmap.md` for tracking).

## Lima guest btrfs support

- Ubuntu version: 24.04.4 LTS (Noble)
- Kernel version: 6.8.0-107-generic
- CONFIG_BTRFS_FS: **m** (module — loads on first mount)
- btrfs-progs version: 6.6.3
- Subvolume snapshot test: **PASS**
- Snapshot time for 100 MB subvolume: **0.154 s** (real)

### CoW divergence test output

```
=== parent ===
hello from parent
diverge parent
=== child ===
hello from parent
```

Child did NOT receive the parent's post-snapshot write (`diverge parent`), confirming true copy-on-write isolation. Child also has its own independent files (`child.txt`).

### Snapshot timing test output

```
real    0m0.154s
user    0m0.003s
sys     0m0.002s
```

Snapshot of a 100 MB subvolume completes in 154 ms — O(1) with respect to data size, as expected.

## Decision

**PROCEED** (Lima driver only) / **BLOCKED** (Firecracker driver — pending custom kernel)

The Lima driver can adopt btrfs subvolume snapshots immediately. The `copyWorkspaceTree` recursive-copy fork path should be replaced with `btrfs subvolume snapshot` for all Lima-backed workspaces. The Lima guest kernel (6.8.0, Ubuntu 24.04) fully supports btrfs as a module; `mkfs.btrfs` and `btrfs-progs` 6.6.3 are available.

The Firecracker driver is **blocked** until a custom kernel is built with `CONFIG_BTRFS_FS=y`. Until then, the Firecracker driver must retain its current fork path or use an alternative CoW strategy (e.g., `cp --reflink=always` on an ext4/XFS volume, or overlayfs). This should be tracked separately and does not block Lima driver work.

Tasks 6, 7, 8 of the refactor plan should proceed targeting the Lima driver. Firecracker btrfs support is a follow-on task.
