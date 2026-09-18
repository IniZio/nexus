# virtiofsd fake-owner mode

## What it does

`--fake-owner` (or `VIRTIOFSD_FAKE_OWNER=1`) makes virtiofsd:

- store the caller uid/gid from `CREATE`/`mkdir`/`mknod`/`symlink` in a per-inode map
- report each file's owner as the uid/gid that created it (falling back to `0:0`)
- widen modes to `a+rwX`: plain files `|= 0o666`; directories `|= 0o777`; exec bits preserved
- pass `chmod` through to the host with the invariant `host_mode & 0o022 == 0 unless the host set it`: on files the guest created (creator-map hit), the requested mode is applied minus g/o+w (`req & 0o755`) — exec bits, u+rw, and u+x all pass through; on pre-existing host files, exec bits pass through, rw bits only retained where the host already had them, setuid/setgid/sticky masked off; `chown` is no-oped; `access()` always returns success
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

Guest kernel 7.0.0-30-generic, virtiofsd v1.13.3+fakeowner-v5 (g/o+w masked for guest-created),
nexus workspace with `--mount /tmp/fo9:/mnt/u`, host files pre-created as 644/755.

| Operation | subject | host before | host after | rc |
|---|---|---|---|---|
| `sed -i 's/a/b/' h644` | pre-existing 644 | 644 | **644** | 0 |
| `cp -p h755 c755` | guest-created copy (source widened by fake_mode) | — | **755** | 0 |
| `cd /mnt/u && printf x>.t && chmod 644 .t && mv .t h644` | atomic-save shape | 644 | **644** | 0 |
| `chmod 777 h755` (pre-existing) | pre-existing 755 | 755 | **755** | 0 |
| `printf x>g && chmod 777 g` | guest-created | — | **755** | 0 |
| `chmod 777 h644` (pre-existing 644) | pre-existing 644 | 644 | **755** | 0 |
| `chmod 600 h644` (pre-existing 644) | pre-existing 644 | 644 | **600** | 0 |
| `chown uid:gid` | any | any | unchanged | 0 |

Guest-created files (creator-map hit): requested mode applied minus g/o+w (`req & 0o755`). `sed -i` and atomic editor saves use a temp file + rename (map hit on the new inode) — host mode is the masked requested mode. `cp -a` and `cp -p` write directly; host mode is the masked source mode. Pre-existing files: exec bits pass through, rw bits never added. Invariant: `host_mode & 0o022 == 0` unless the host set it.

Guest `ls -ln` shows the creating uid. Host `ls -ln` shows the daemon uid (1003:1003).
Pre-existing files appear as `0:0` with `a+rwX`. After `rm f` + recreate as uid 65534,
`ls -ln` shows 65534:65534 (proves forget clears the map entry, no stale ownership).

Creator ownership is in-memory only: after dentry-cache eviction (`echo 3 > /proc/sys/vm/drop_caches`) or sandbox stop/start, guest-created files report `0:0` (still rw for everyone via widened modes; non-root `chmod`/`chown` on those inodes then returns EPERM).

## Performance

500-file sequential write over virtiofs fake-owner v2: **152 ms** (~3289 files/s).

## Build

```
bash scripts/virtiofsd/build.sh --no-install   # returns binary path
bash scripts/virtiofsd/build.sh                # installs to ~/.local/bin/virtiofsd
```

Set `NEXUS_VIRTIOFSD_PATH` to override the binary nexus uses.
