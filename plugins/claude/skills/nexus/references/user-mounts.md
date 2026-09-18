# User mounts: sharing your host agent setup into sandboxes

nexus has **no built-in default mounts** — it ships no hardcoded tool list.
All host-to-guest sharing is driven entirely by user config.

Pass `--no-user-mounts` on a `create` call to suppress all user-global mounts
for that sandbox.

## User-global config: `~/.config/nexus/config.yaml`

The file lives at `$XDG_CONFIG_HOME/nexus/config.yaml` (falls back to
`~/.config/nexus/config.yaml` when `$XDG_CONFIG_HOME` is unset).

It uses `version: 1` and the `sandbox.mounts` key.

Both **short form** (`host:guest[:ro]`) and **long form**
(`{source, target, read_only}`) are accepted — same syntax as Docker Compose
bind-mounts. `~` and `$HOME` expand against the operator's home directory.

```yaml
version: 1

sandbox:
  mounts:
    # Short form: host:guest:ro
    - ~/.claude/plugins:/root/.claude/plugins:ro
    - ~/.local/bin:/root/.local/bin:ro
    - ~/.local/share/mise:/root/.local/share/mise:ro
    - ~/.local/share/uv:/root/.local/share/uv:ro
    - ~/.bun:/root/.bun:ro
    - ~/.vscode-server/extensions:/root/.vscode-server/extensions:ro
    - ~/.local/share/groundwork:/root/.local/share/groundwork:ro
    # Long form (exercises both YAML shapes)
    - source: ~/.codegraph
      target: /root/.codegraph
      read_only: true
```

A parse error in the config file is **logged** and the sandbox starts without
user mounts — `sandbox create` never fails due to a bad config.

## Platform compatibility

Host and guest are both **Linux x86_64**, so mounted ELF binaries and shared
libraries run without translation.

## Diagnosis: my tool / plugin / MCP isn't working

Run these two commands against a running sandbox:

```sh
nexus exec <ref> -- sh -lc 'claude mcp list'
nexus exec <ref> -- sh -lc 'claude plugin list'
```

Interpret the output:

| Symptom | Cause | Fix |
|---|---|---|
| `ENOENT` / not found in `$PATH` | Tool binary or shim dir not in guest | Add a `sandbox.mounts` entry for the tool's install dir |
| Plugin `cache-miss` | Marketplace source dir absent in guest | Add a `sandbox.mounts` entry for the source dir |
| MCP server crashes / exits immediately | Server binary resolves but runtime env missing | Add a `sandbox.mounts` entry for the runtime (e.g. the `uv` or `mise` tree the server's wrapper invokes) |

## Security boundary

Never add credential directories to `sandbox.mounts`. All virtiofs mounts are
host-read-only inside the guest, but read-only is not the same as invisible —
an in-guest agent can read anything mounted. The following must **never** appear
in user config:

- `~/.ssh`
- `~/.config/gh`
- `~/.claude.json` / `~/.claude/.credentials.json`
- `~/.aws`, `~/.config/gcloud`, or any provider credential store

The security model rests on **"tool payloads, never credential stores."**

---

## Container uid contract

### fake-owner mode (default when patched virtiofsd is installed)

`scripts/virtiofsd/build.sh` installs a patched virtiofsd with `--fake-owner`
support. The nexus virtiofs driver probes `virtiofsd --help` at sandbox start and
enables `--fake-owner` automatically (override with `NEXUS_VIRTIOFS_FAKE_OWNER=0`).

What fake-owner guarantees:

- Any guest uid can read and write all files in the mount
- Host file ownership is unchanged — all guest writes land as the daemon uid (host owner)
- Exec bits are preserved; `a+rwX` is reported so directories are always traversable
- `chown` requests are silently accepted (no-op) and return success

**Limitation — chmod/chown by non-root guest processes:** files appear owned by
`0:0` in the guest. The guest kernel's VFS layer (`may_setattr` →
`inode_owner_or_capable`) blocks `chmod` and `chown` from any process that is
not uid 0 and lacks `CAP_FOWNER`, before the FUSE request reaches virtiofsd.
Both return `EPERM`. Because fake-owner widens modes to `a+rwX`, workload
access is unaffected by what `chmod` would set. Steps that require `chmod` must
run as guest root, or use the `NEXUS_HOST_UID` recipe below.

Root cause: the guest kernel sends `FUSE_UNKNOWN_UID` (`0xFFFFFFFF`) in all
FUSE request headers under the nexus vhost-user virtiofs stack; virtiofsd cannot
recover the caller's uid to report per-caller ownership.

### Without fake-owner / per-uid matching

If fake-owner is disabled (`NEXUS_VIRTIOFS_FAKE_OWNER=0`) or the unpatched
distro virtiofsd is in use, every guest uid must match the host owner to write
shared directories.

nexus seeds two variables into every guest at boot:

| Variable | Value |
|---|---|
| `NEXUS_HOST_UID` | numeric uid of the host user (`os.Getuid()` of the supervisor) |
| `NEXUS_HOST_GID` | numeric gid of the host user (`os.Getgid()` of the supervisor) |

Both contexts receive them: login shells (via `/etc/profile.d/nexus-cred.sh`) and
non-login `nexus exec` sessions (via `/etc/nexus/hostuid.env`, merged into every
exec's baseline environment).

**Compose recipe**

```yaml
services:
  app:
    user: "${NEXUS_HOST_UID}:${NEXUS_HOST_GID}"
    build:
      args:
        USER_ID: ${NEXUS_HOST_UID}
```

Guest root (`user: "0:0"` or omitting `user:`) needs no special handling in either mode.
