---
id: HPO-R-011
type: requirement
concept: C-HPO
criticality: must
verification: manual
status: active
origin_decision_ref: herdr-plugin-ootb#D-14, herdr-plugin-ootb#D-15
summary: "Focus-scoped forwarding reads the host session; doctor reports (not kills) duplicate daemons; startup hook reaps only its own pidfile process."
---

## HPO-R-011 — Host-session focus; doctor reports duplicates; startup reaps own daemon {#hpo-r-011}

**Given** herdr 0.9.0 protocol 22, which exposes no client identity channel (`workspace_focused` = `{type, workspace_id}`; `api snapshot` has a single session-global `focused_workspace_id`; pane env carries no client id; the Mac remote client stores only `endpoint-selection.json` and `endpoints.json` locally — confirmed by read-only probe 2026-09-16):

1. `clientagent.Tick` **shall** derive the focused workspace from `~/.local/state/nexus3/portfwd/focus.state`, a file written atomically by the host plugin on every `workspace.focused` event, rather than from a per-client API call. Focus-scoped forwarding is accurate when the remote `nexus3-client` is the **sole interactive client** on that herdr session; when both a host TUI and a remote client are attached, the host session's last `workspace.focused` write wins.

2. `nexus3 herdr doctor` **shall** detect and report duplicate herdr server processes, remote-client-bridge processes, and `nexus3-client` daemon processes with a human-readable remediation command, and **shall not** kill any process it does not own (i.e., it **shall not** terminate herdr server or remote-client-bridge processes from plugin code).

3. The macOS `[[startup]]` hook (`nexus3-client herdr local-agent-startup`) **shall** kill at most one previous `nexus3-client` instance — the one whose PID is recorded in the plugin's own pidfile under `$HERDR_PLUGIN_STATE_DIR` — and **shall not** kill herdr-owned processes.

- **Why (D-14)** — herdr 0.9.0 provides no client identity channel; a per-client focus API does not exist. Using a host-written `focus.state` file delivers correct semantics for the common single-client case and avoids a per-tick `herdr workspace list` ssh round-trip. The sole-interactive-client constraint must be documented rather than enforced in code, because the protocol cannot distinguish clients.
- **Why (D-15)** — killing herdr server or remote-client-bridge processes from a plugin hook risks tearing down the operator's entire terminal session. Live probe 2026-09-16 found a ghost herdr server (Sep 10) and three `remote-client-bridge` processes on `engine-03`; only the plugin's own daemon is the plugin's to manage.
- **Fit criterion** — (a) `focus.state` is written within one reconcile tick of a `workspace.focused` event and read by `clientagent.Tick` without issuing any `herdr workspace list` call; (b) `nexus3 herdr doctor` output includes entries for each duplicate class with a remediation command, and the implementation contains no `SIGKILL`/`SIGTERM` directed at herdr server or bridge pids; (c) a second `nexus3-client herdr local-agent-startup` invocation kills the pid from the existing pidfile and no other process.
- **Verification**: manual — verified by live probe 2026-09-16 (D-14 rationale); automated coverage is blocked on a herdr client-identity protocol extension (see `docs/upstream/herdr-client-id.md`).
- **Criticality**: must
- **See also** [HPO-R-008](#hpo-r-008), [HPO-R-010](#hpo-r-010)
