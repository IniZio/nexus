# virtiofsd fake-owner mode

## What it does

`--fake-owner` (or `VIRTIOFSD_FAKE_OWNER=1`) makes virtiofsd report every file
as owned by `0:0` and widens modes to `a+rwX`:

- plain files: mode `|= 0o666`; exec bits preserved if any were set
- directories: mode `|= 0o777`
- `access()` always returns success
- `chown` requests are silently accepted (no-op)
- uid/gid squash mapping: all guest uids/gids map to the daemon's host uid/gid

The nexus virtiofs driver (`ch_virtiofs.go`) auto-detects `--fake-owner` via
`--help` probe and passes the flag automatically. Override with
`NEXUS_VIRTIOFS_FAKE_OWNER=0` to suppress it.

## Known limitation: chmod by non-root guest processes

Files appear as `0:0` in the guest. The Linux VFS layer (`may_setattr` →
`inode_owner_or_capable`) blocks `ATTR_MODE` setattr from any process that is
not uid 0 and lacks `CAP_FOWNER`, before the FUSE request reaches virtiofsd.
Result: `chmod` by uid 1000 or uid 65534 returns `EPERM`.

Because fake-owner unconditionally widens modes to `a+rwX`, the guest always
has read+write access regardless of what `chmod` sets. The `EPERM` is visible
but has no practical effect on workload access.

`chown` is separately intercepted in virtiofsd and silently succeeds regardless
of caller uid; that path does not go through the VFS ownership check.

## Caller-uid finding

FUSE `getattr` requests arrive with `ctx.uid = 4294967295` (`FUSE_UNKNOWN_UID`).
The kernel issues these from VFS cache management paths, not from a user
process, so the caller uid is unavailable. Files must be reported with a static
uid; `0:0` is chosen so that root-owned tooling in the guest works without
adjustment.

## Write matrix (integration test results)

Guest kernel 6.12.76, virtiofsd v1.13.3+fakeowner, nexus workspace with
`--mount /tmp/fo2:/mnt/u`.

| Operation | uid 0 | uid 1000 | uid 65534 |
|---|---|---|---|
| touch | ✓ | ✓ | ✓ |
| echo > (create) | ✓ | ✓ | ✓ |
| echo >> (append) | ✓ | ✓ | ✓ |
| chmod 600 | ✓ | EPERM† | EPERM† |
| chown 1000:1000 | ✓ | EPERM†† | EPERM†† |
| mkdir | ✓ | ✓ | ✓ |
| cp (data) | ✓ | ✓ | ✓ |
| git status | ✓ | — | — |

† VFS-layer block, not virtiofsd; modes are `a+rwX` regardless.
†† chown for non-root is a VFS-layer block on the fake-owner ATTR_UID/GID path.

## Performance

500-file sequential write over virtiofs: **188 ms** (~2660 files/s).

## Build

```
bash scripts/virtiofsd/build.sh --no-install   # returns binary path
bash scripts/virtiofsd/build.sh                # installs to ~/.local/bin/virtiofsd
```

Set `NEXUS_VIRTIOFSD_PATH` to override the binary nexus uses.
