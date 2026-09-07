# Sandbox lifecycle

nexus3 creates and manages isolated microVMs. Each sandbox has its own network, filesystem, and process namespace.

## Commands

```sh
nexus3 create [--file <Dockerfile>] [--mount <host-path>:<guest-path>[:ro]] [--mount-named <name>:<guest-path>[:<options>]] ...
nexus3 ps
nexus3 start <sandbox-ref>
nexus3 stop <sandbox-ref>
nexus3 pause <sandbox-ref>
nexus3 resume <sandbox-ref>
nexus3 rm <sandbox-ref>
nexus3 shell <sandbox-ref>
nexus3 forward <sandbox-ref> <host-port>:<guest-port>
```

## Live host mounts (`--mount`)

`--mount <host-path>:<guest-path>[:ro]` mounts a host directory into the guest as a live virtiofs share. Edits inside the sandbox appear on the host immediately — no archive or sync step. Repeatable.

Primary use-case: mounting a git worktree so an in-guest agent can edit source files and commit directly to the host branch.

**Key rules:**
- The host path must exist and be a directory; it is resolved to an absolute path.
- `.git` guest paths are **allowed** (D-PD-99) — this is the deliberate divergence from `--mount-named`. Mounting a real worktree's `.git` is the primary use-case.
- `fork` and `snapshot create` are **refused** on a sandbox with live mounts; the error names the offending host→guest pairs.

**Contrast with `--mount-named`:**

| | `--mount` | `--mount-named` |
|---|---|---|
| Backing | Host directory via virtiofs | User-owned volume (ext4 disk or virtiofs dir) |
| Persistence | Host filesystem | Volume store (`nexus3 volume rm` to delete) |
| `.git` guest path | Allowed | **Hard refused** |
| Fork / snapshot | Refused | Refused (TBR-PD-15, deferred) |
| Use case | Live worktree editing | Dependency stores, build caches |

Do **not** use `--mount` to mount dependency directories (node_modules, target, etc.) — use `--mount-named kind=disk` for those; block I/O is measurably faster for metadata-heavy operations.

For named volumes (`--mount-named`) see `references/volumes.md`.
