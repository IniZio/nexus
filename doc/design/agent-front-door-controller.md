# Agent front door: controller (P1)

Design notes for `nexus-controller` (Slack front door) and its live test
harness. The P1 spec lives in `.groundwork/design/agent-front-door.md`; this
page records the implementation rationale that was moved out of code comments.

## Thread lifecycle

One Slack thread is one session: one task record, one worktree sandbox, one
herdr agent.

| Status | Entered when | Reply / mention does |
|---|---|---|
| `working` | a turn is running | nothing (agent busy) |
| `idle` | turn finished or timed out | re-prompts the same agent |
| `waiting_on_user` | agent is blocked on a question | feeds the answer (digit = menu key) |
| `paused` | idle for `idle_pause` | resumes the sandbox, then prompts |
| `stopped` | paused for `idle_stop` | starts the sandbox, relaunches the agent, then prompts |
| `failed` | provision failed | mention reopens |
| `closed` | PR merged/closed or explicit close | dropped (terminal) |

- Stop is not remove: a stopped sandbox keeps its disk so the next reply can
  revive it. `closed` is reserved for real termination.
- Idle thresholds are per channel (`channels.<id>.idle_pause` / `idle_stop`,
  defaults 30m / 4h; zero disables that step). The tick lists candidates older
  than the smallest pause across channels and each task applies its own
  channel's thresholds. The tick interval is half the smallest pause, clamped
  to 1s..30m.
- A turn is bounded by `Deps.TurnTimeout` (default 30m; the spec sets none).
  On expiry the task returns to `idle` with a warning so the next reply can
  continue, instead of staying `working` forever.

## Identity

- Every reply and mention requires a linked user (`Linker.Require`).
- Only the task owner can drive a thread. Another user's message is refused
  with a mention of the owner; the agent is never prompted under the owner's
  sandbox identity for someone else.
- The herdr backend compares the requested principal with the sandbox record's
  principal and fails on empty or mismatch; the herdr plugin refuses to adopt an
  orphan sandbox whose principal differs from `NEXUS_PRINCIPAL`.
- Linked records carry `allowed_projects` set by the linking surface, not the
  connector: laptop mode (default) links with `*`; shared mode links with an
  empty list (deny until an operator grants projects). The 401 force-refresh
  path enforces `allowed_projects` before handing a token to the broker.

## Serve

`Serve` fails closed before starting when the config file is missing or
invalid, a Slack token ref does not resolve, no channels are configured, an
inline secret is present, or the vault key is unavailable. A non-nil handler
returned by the deps factory becomes the router's slash-command handler.

## Live test harness (`internal/testutil/livenexus`)

- Isolation: `XDG_CONFIG_HOME` points at `<base>/config` so the harness herdr
  server loads a harness-owned copy of the nexus plugin built from the
  worktree; its shim honours `NEXUS_BIN`, so panes run in the guest VM. The
  session socket is `<base>/config/herdr/sessions/<session>/herdr.sock`.
- Images: only content-addressed `sha256/` blobs are hard-linked (opened
  read-only by the OCI store). Everything else (`*.ext4`, `*.img`, anything a
  VM or builder may open read-write) is copied sparse so test writes never
  mutate prod inodes.
- The test binary is built to a temp file and renamed into place, so concurrent
  packages still running the old inode are unaffected.
- Prod invariants (`nexus ps`, main repo branch) are snapshotted with the
  installed prod `nexus` from `PATH`, resolved before env isolation. Snapshot
  fields are scoped by base and session so concurrent harnesses do not report
  each other as leaks.
- Cleanup order: preserve logs; rm tracked sandboxes; sweep remaining sandboxes
  in the isolated root; wait for supervisors referencing the root; stop the
  herdr unit and remove its session dir; remove the root; kill and report any
  process still referencing the root; compare prod invariants and fail on any
  difference.
