# virtiofsd — pinned build

| Item | Value |
|---|---|
| Upstream tag | v1.13.3 |
| Tag object SHA | 13ee2e13024eaf40cdedb4bcb0a49d5695ade0b8 |
| Commit SHA | bbf82173682a3e48083771a0a23331e5c23b4924 |
| Patch file | fakeowner.patch |
| Patch SHA-256 | f2c7a7f8b2e75fa1850447f14b87bfc66ad0494bcc748e802b6298681ee8ecbf |
| Patch lines | 383 |
| Embedded binary SHA-256 (amd64 musl static) | 6ec8fd513874e28825c18b0107fc43599350c4166bf6fe9dd0adc97f4bde3e6e |
| Build environment | Alpine 3.20 inside nexus sandbox; Rust 1.98.1 stable; x86_64-unknown-linux-musl target; libseccomp 2.5.5 + libcap-ng 0.8.5 both static (apk libseccomp-static + libcap-ng-static) |
| Reproducibility | SHA confirmed identical across two independent builds from the same source |
| Hostbin version string | 1.13.3-fakeowner |
| Release URL (TODO: not yet uploaded) | https://github.com/IniZio/nexus/releases/download/virtiofsd-v1.13.3-fakeowner/virtiofsd-amd64 |

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
to `0:0` for inodes not in the map). `chown` is no-oped on the host; `chmod` on files the guest created passes the requested mode through unchanged (mask 0o7000); on pre-existing host files it applies narrowing (exec bits pass through, rw bits only retained where the host already had them, setuid/setgid/sticky masked off). The guest VFS allows owner-matching ops because `inode_owner_or_capable` sees the reported uid matching the caller. Result: `echo x > f && chmod +x f && chown 1000:1000 f &&
cp -p f g` as guest uid 1000 returns `rc=0` and `ls -ln` shows `1000:1000`.

## Rebuild (host dynamic binary for PATH install)

```
bash scripts/virtiofsd/build.sh           # build + install to ~/.local/bin/virtiofsd
bash scripts/virtiofsd/build.sh --no-install  # build only; prints binary path
```

## Rebuild (static musl binary for hostbin embed)

Build inside a nexus Alpine 3.20 sandbox with the Rust musl toolchain.
The `make artifacts VIRTIOFSD_DIR=<dir>` path reads the pre-built binary,
verifies it against the pinned SHA above, and compresses it into the embed.

```
make artifacts VIRTIOFSD_DIR=/path/to/dir/containing/virtiofsd
```
