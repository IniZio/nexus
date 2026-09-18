# virtiofsd — pinned build

| Item | Value |
|---|---|
| Upstream tag | v1.13.3 |
| Tag object SHA | 13ee2e13024eaf40cdedb4bcb0a49d5695ade0b8 |
| Commit SHA | bbf82173682a3e48083771a0a23331e5c23b4924 |
| Patch file | fakeowner.patch |
| Patch SHA-256 | 0c3f5cb8521f0586ed88e86f188ee74c573dd43cc2b9e35cf11483536228eb8e |
| Patch lines | 235 |

## Rationale

virtiofsd is vendored as a patched build rather than distro-packaged for two reasons:

1. The `--fake-owner` mode required here (see README.md) is not upstream.
2. Distro virtiofsd packages on Ubuntu 24.04 ship v1.9.x, which lacks uid-map
   squash support needed for correct host-ownership isolation.

## Caller-uid finding

When virtiofsd runs without `--sandbox` (nexus default: `--sandbox=none`),
FUSE `getattr` requests from the guest kernel arrive with `ctx.uid = 4294967295`
(`u32::MAX`, `FUSE_UNKNOWN_UID`). This is a kernel-internal sentinel: the kernel
issues getattr on behalf of VFS cache management, not on behalf of a specific
user process, so no meaningful uid is available. Confirmed by one-shot probe
written to `/tmp/virtiofsd-uid-probe.txt` during integration testing (removed
before patch was finalised).

Consequence: the daemon cannot dynamically report each file as owned by its
caller. All files are reported as `0:0` in fake-owner mode, with widened modes
(`a+rwX`). This makes write and read accessible to any guest uid; however,
`chmod` and `chown` from non-root guest processes fail at the guest kernel VFS
layer (`may_setattr` / `inode_owner_or_capable`) before the FUSE request
reaches the daemon. This is a known limitation documented in README.md.

## Rebuild

```
bash scripts/virtiofsd/build.sh           # build + install to ~/.local/bin/virtiofsd
bash scripts/virtiofsd/build.sh --no-install  # build only; prints binary path
```
