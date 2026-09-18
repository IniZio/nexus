# virtiofsd — pinned build

| Item | Value |
|---|---|
| Upstream tag | v1.13.3 |
| Tag object SHA | 13ee2e13024eaf40cdedb4bcb0a49d5695ade0b8 |
| Commit SHA | bbf82173682a3e48083771a0a23331e5c23b4924 |
| Patch file | fakeowner.patch |
| Patch SHA-256 | 603600cf03134eb6cfab6a7f8b63b5d302dccc4a3584f313c131778815543c0f |
| Patch lines | 372 |

## Rationale

virtiofsd is vendored as a patched build rather than distro-packaged for two reasons:

1. The `--fake-owner` mode required here (see README.md) is not upstream.
2. Distro virtiofsd packages on Ubuntu 24.04 ship v1.9.x, which lacks uid-map
   squash support needed for correct host-ownership isolation.

## Caller-uid finding

`CREATE` (opcode 35) carries the real caller uid in `InHeader.uid` (confirmed
empirically via raw-header probe on kernel 7.0.0-30-generic, vhost-user,
`--sandbox=none`). All other FUSE ops (`LOOKUP`, `GETATTR`, `SETATTR`,
`OPENDIR`, `READDIR`) arrive with `uid = 4294967295` (`FUSE_UNKNOWN_UID`).

The patch stores the caller uid from `CREATE`/`mkdir`/`mknod`/`symlink` in a
per-inode `HashMap<u64,(u32,u32)>` on `PassthroughFs`. Every attr-returning
path (`lookup`, `getattr`, `setattr`, `link`) reads from that map (falling back
to `0:0` for inodes not in the map). `chown` is no-oped on the host; `chmod` applies with mode narrowing: exec bits pass through as requested, rw bits are only retained where the host already had them (can remove, never add), setuid/setgid/sticky masked off. Pre-existing host files not owned by the daemon return EPERM for non-root `chmod` (guest sees 0:0 owner).
the guest VFS allows owner-matching ops because `inode_owner_or_capable` sees the reported uid
matching the caller. Result: `echo x > f && chmod +x f && chown 1000:1000 f &&
cp -p f g` as guest uid 1000 returns `rc=0` and `ls -ln` shows `1000:1000`.

## Rebuild

```
bash scripts/virtiofsd/build.sh           # build + install to ~/.local/bin/virtiofsd
bash scripts/virtiofsd/build.sh --no-install  # build only; prints binary path
```
