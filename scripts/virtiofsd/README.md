# virtiofsd fake-owner mode

## What it does

`--fake-owner` (or `VIRTIOFSD_FAKE_OWNER=1`) makes virtiofsd:

- store the caller uid/gid from `CREATE`/`mkdir`/`mknod`/`symlink` in a per-inode map
- report each file's owner as the uid/gid that created it (falling back to `0:0`)
- widen modes to `a+rwX`: plain files `|= 0o666`; directories `|= 0o777`; exec bits preserved
- no-op `chmod` and `chown` on the host; `access()` always returns success
- squash all guest uids/gids to the daemon's host uid/gid for real host ops

The nexus virtiofs driver (`ch_virtiofs.go`) auto-detects `--fake-owner` via
`--help` probe and passes the flag automatically. Override with
`NEXUS_VIRTIOFS_FAKE_OWNER=0` to suppress it.

## Caller-uid finding

`CREATE` (opcode 35) carries the real caller uid in `InHeader.uid`. All other
FUSE ops (`LOOKUP`, `GETATTR`, `SETATTR`, `OPENDIR`, `READDIR`) arrive with
`uid = 4294967295` (`FUSE_UNKNOWN_UID`). Confirmed empirically via raw-header
probe on guest kernel 7.0.0-30-generic, vhost-user, `--sandbox=none`.

The patch stores the uid on create and serves it back on every subsequent attr
call. The guest VFS's `may_setattr` / `inode_owner_or_capable` check succeeds
because the reported uid matches the caller.

## Write matrix (integration test results)

Guest kernel 7.0.0-30-generic, virtiofsd v1.13.3+fakeowner-v2 (creator-uid map),
nexus workspace with `--mount /tmp/fo5:/mnt/u`, uid-1000 via `setpriv --reuid=1000`.

| Operation | uid 0 | uid 1000 | uid 65534 |
|---|---|---|---|
| echo > (create) | ✓ rc=0 | ✓ rc=0 | ✓ rc=0 |
| echo >> (append) | ✓ rc=0 | ✓ rc=0 | ✓ rc=0 |
| mkdir | ✓ rc=0 | ✓ rc=0 | ✓ rc=0 |
| cp -p (preserve mode+owner) | ✓ rc=0 | ✓ rc=0 | ✓ rc=0 |
| chmod +x | ✓ rc=0 | ✓ rc=0 | ✓ rc=0 |
| chown uid:gid | ✓ rc=0 | ✓ rc=0 | ✓ rc=0 |
| rm then recreate → new owner | — | 65534 after uid-65534 recreate ✓ | — |
| git status (clean repo) | clean ✓ | clean ✓ | — |
| host ls -ln (daemon uid) | 1003:1003 ✓ | 1003:1003 ✓ | 1003:1003 ✓ |

Guest `ls -ln` shows the creating uid. Host `ls -ln` shows the daemon uid (1003:1003).
Pre-existing files appear as `0:0` with `a+rwX`. After `rm f` + recreate as uid 65534,
`ls -ln` shows 65534:65534 (proves forget clears the map entry, no stale ownership).

## Performance

500-file sequential write over virtiofs fake-owner v2: **152 ms** (~3289 files/s).

## Build

```
bash scripts/virtiofsd/build.sh --no-install   # returns binary path
bash scripts/virtiofsd/build.sh                # installs to ~/.local/bin/virtiofsd
```

Set `NEXUS_VIRTIOFSD_PATH` to override the binary nexus uses.
