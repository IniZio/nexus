# Destructive-op safety — prune, trash, and audit

**Branch**: fix/prune-cross-session  
**Status**: authoritative for the prune session-scope, trash, and audit-log designs

Background: on 2026-09-26 a cross-session `nexus herdr prune -apply` reaped two
prod sandboxes and permanently deleted their agentcfg volumes (Claude transcripts).
See `.groundwork/debug/2026-09-26-sandbox-vanished.md` for the full incident
analysis.

---

## Session-scoped herdr bindings

**Decision**: `HerdrSpaceBinding` records the herdr socket path that created the
binding (`herdr_session` field). The canonical value is: `HERDR_SOCKET_PATH` if
set, else `~/.config/herdr/sessions/<HERDR_SESSION>/herdr.sock` if
`HERDR_SESSION` is set, else `~/.config/herdr/herdr.sock` (the default session).
Prune judges workspace existence only for bindings whose `herdr_session` matches
the current session. Bindings from a foreign session or legacy bindings with an
empty field are treated as alive unconditionally.

**Why**: the incident showed that prune asked the current session (`debug2`) which
workspaces exist. `debug2` knew only `w1`–`w3`, so it judged `wCZ` and `wD7` gone
and reaped them. Those sandboxes belonged to the default session. Recording the
owning session turns a silent mis-attribution into an explicit scope filter.

**Scoped `--workspace` prune**: the worktree.removed hook passes a short id like
`w1`. Short ids are not globally unique — two sessions can each have a `w1` — so
a scoped prune must also filter by `herdr_session`. A match requires both the
workspace id and the session to agree.

---

## Reversible-first prune

**Decision**: reaping a worktree sandbox (VM + volumes) requires positive evidence
that the worktree checkout path (`worktree_path` on the binding) is absent:
`os.Stat(worktree_path)` must return `os.ErrNotExist`. Any other outcome — stat
error, the path present, the path empty, or `worktree_path` itself empty — keeps
the sandbox alive and only clears the stale workspace id. A `workspace.close`
failure from herdr is never treated as evidence of absence.

**Why**: the incident reaper assumed "workspace not in this session ⇒ worktree
gone". The checkout was present on disk and the sandboxes were live. Requiring
`os.ErrNotExist` on the checkout path makes the reaper falsifiable: it can only
act when the filesystem agrees the checkout is gone. Stat errors (permissions,
I/O) are not absence — they are unknown state; fail-safe means keep.

---

## Global prune guard

**Decision**: `prune --apply` without `--workspace` refuses when:
- `HERDR_SOCKET_PATH` or `HERDR_SESSION` selects a non-default session, or
- the binding store contains bindings from another `herdr_session`.

The user must pass `--allow-foreign-sessions` to override. The flag exists to
support deliberate cross-session cleanup (e.g. a dead session's bindings after
its socket is gone) without making that mode the default.

**Why**: the incident command was run with `HERDR_SOCKET_PATH` set to a debug
session. A global prune from a non-default session is almost never intentional.
Refusing by default converts a common mistake into an explicit opt-in.

---

## Trash, not delete, for agentcfg and nexusstate

**Decision**: `Service.Remove` and the prune path move agentcfg and nexusstate
volumes to `<state>/volumes/.trash/<name>@<UTC-ts>` instead of deleting them.
Entries older than 7 days are expired opportunistically on the prune path. Cache
volumes (docker, gocache, gopath) are rebuildable and continue to be deleted.

Recovery surface:
- `nexus volume ls --trash` — list trashed entries with age and size.
- `nexus volume restore <entry> [--as name]` — move an entry back to `volumes/`.

**Why**: the incident deleted agentcfg permanently. Those volumes hold the only
copy of in-guest Claude transcripts. A 7-day trash window is cheap (volumes
compress well, and the user can force-expire early) and turns a permanent loss
into a recoverable one. Cache volumes are excluded because they are reconstructed
on next use and trashing them would double storage without benefit.

---

## Destructive audit log

**Decision**: every call to `Service.Remove` and every `volumestore` Rm, Trash,
Restore, or expiry appends a JSON line to `<state>/audit/destructive.jsonl`
(mode 0600). The log is append-only; nothing in nexus truncates or rotates it.

Fields per entry:

| field | content |
|---|---|
| `ts` | RFC3339Nano timestamp |
| `op` | `remove`, `vol.rm`, `vol.trash`, `vol.restore`, `vol.expire` |
| `sandbox_id` | workspace handle (e.g. `handbook-review/newman-cto-104`) |
| `handle` | volume name or path |
| `volumes` | slice of volume names affected |
| `reason` | caller-supplied string from context; `"unspecified"` if absent |
| `argv` | `os.Args` of the nexus process |
| `pid` / `ppid` | process and parent ids |
| `cwd` | working directory at process start |
| `herdr_socket_path` | resolved `herdr_session` value |
| `err` | error string, or omitted on success |

One `slog.Info` line mirrors each entry for operators watching the structured log.
Audit write failures are logged and ignored — they never block the operation
(fail-open). A blocked or full filesystem must not prevent necessary cleanup.

**Why**: the incident had no server-side record. `Service.Remove` was silent;
prune's REAPED lines went only to stdout; `volumestore.Rm` was silent. The audit
log gives any future incident a forensic trail without requiring the caller to
have captured stdout.

---

## Out of scope / follow-up

Continuous host-side mirroring of guest `/root/.claude/projects` is a separate
concern tracked in `.groundwork/design/guest-transcript-sync.md`. The trash
window narrows the loss window but does not close it: a transcript written between
the last sync and the remove is still lost if the trash entry expires before
anyone notices.
