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

All FUSE requests arrive with `ctx.uid = 4294967295` (`FUSE_UNKNOWN_UID`).
Confirmed empirically across `lookup`, `create`, and `getattr` on guest kernel
7.0.0-30-generic with the nexus vhost-user virtiofs stack (`--sandbox=none`).
A TRUE-fakeowner build (returns `ctx.uid` when valid, falls back to 0) produced
the same 0:0 output, proving `FUSE_UNKNOWN_UID` is sent for all operations.
Files are reported with static `0:0` as the owner.

## Write matrix (integration test results)

Guest kernel 7.0.0-30-generic, virtiofsd v1.13.3+fakeowner, nexus workspace
with `--mount /tmp/fo4:/mnt/u`, uid-1000 via `setpriv --reuid=1000`.

| Operation | uid 0 | uid 1000 |
|---|---|---|
| echo > (create) | ✓ | ✓ |
| echo >> (append) | ✓ | ✓ |
| mkdir | ✓ | ✓ |
| cp (data) | ✓ | ✓ |
| cp -p (preserve mode) | ✓ | EPERM† |
| chmod +x | ✓ | EPERM† |
| chown uid:gid | ✓ | EPERM† |

† VFS-layer block (`inode_owner_or_capable`): file reports owner 0:0, caller has
no `CAP_FOWNER`. virtiofsd never receives the request. Modes are `a+rwX`
regardless, so read/write/exec access is unaffected.

## Performance

500-file sequential write over virtiofs: **188 ms** (~2660 files/s).

## Build

```
bash scripts/virtiofsd/build.sh --no-install   # returns binary path
bash scripts/virtiofsd/build.sh                # installs to ~/.local/bin/virtiofsd
```

Set `NEXUS_VIRTIOFSD_PATH` to override the binary nexus uses.
