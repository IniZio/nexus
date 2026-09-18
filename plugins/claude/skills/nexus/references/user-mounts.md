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

Every guest's shared directories are owned by the host user (virtiofsd runs as that
user; guest root is fine because virtiofsd skips the credential switch for uid 0).
Docker-compose containers that run as a **fixed non-root uid** (e.g. `USER_ID=1000`)
need to match the host owner to write those dirs — rootless virtiofsd provides no
ownership virtualisation.

nexus seeds two variables into every guest at boot:

| Variable | Value |
|---|---|
| `NEXUS_HOST_UID` | numeric uid of the host user (`os.Getuid()` of the supervisor) |
| `NEXUS_HOST_GID` | numeric gid of the host user (`os.Getgid()` of the supervisor) |

Both contexts receive them: login shells (via `/etc/profile.d/nexus-cred.sh`) and
non-login `nexus exec` sessions (via `/etc/nexus/hostuid.env`, merged into every exec's
baseline environment). `/etc/nexus/startup` boot tasks run before the supervisor seeds the file, so `NEXUS_HOST_UID` is available to exec/login sessions, not to image startup hooks.

**Compose recipe**

```yaml
services:
  app:
    user: "${NEXUS_HOST_UID}:${NEXUS_HOST_GID}"   # runtime uid — matches host owner
    build:
      args:
        USER_ID: ${NEXUS_HOST_UID}                 # bake-time, devcontainer style
```

Guest root (`user: "0:0"` or omitting `user:`) needs no special handling.
