---
id: HPO-R-008
type: requirement
concept: C-HPO
criticality: must
verification: automated
status: active
origin_decision_ref: herdr-plugin-ootb#D-8
summary: "Remote-client port-forwards are scoped to the focused workspace; unfocused sandbox forwards are not applied."
---

## HPO-R-008 — Remote-client port-forwards scoped to the focused workspace {#hpo-r-008}

**When** a macOS remote client is running and the user unfocuses a worktree workspace, `clientagent.Tick` **shall** remove the forwards belonging to that workspace's bound sandbox from `127.0.0.1` within one reconcile tick (≤5 s); **when** the user focuses a worktree workspace, `clientagent.Tick` **shall** apply that workspace's sandbox forwards within one reconcile tick; `clientagent.Tick` **shall not** apply forwards belonging to a sandbox whose workspace is not currently focused; and **when** `focus.state` is absent or its `sandbox_id` is empty, `clientagent.Tick` **shall** treat the desired forward set as empty — cancelling any previously applied forwards and applying nothing.

- **Why** — without focus scoping, forwards from every sandbox accumulate on the client for the lifetime of the guest TCP listener, causing port collisions on shared port numbers (e.g. 3000, 5432) across concurrent worktrees and leaking services that the user has switched away from.
- **Fit criterion** — a unit test wires a fake `herdr workspace.focused` event source and a fake portfwd manager, then asserts: (a) after a focus event for workspace W, only W's sandbox forwards are in the applied set; (b) after an unfocus or a focus-switch to a different workspace, W's forwards are removed within one Tick call; (c) forwards of unfocused sandboxes are never passed to `Manager.Reconcile`; (d) when `focus.state` is absent or `sandbox_id` is empty, the Reconcile desired set is empty and any previously applied forwards are cancelled.
- **Constraint (D-14)** — herdr 0.9.0 protocol 22 carries no client identity: `workspace_focused` delivers `{type, workspace_id}` with no client field, the `api snapshot` exposes a single session-global `focused_workspace_id`, and pane env variables (`HERDR_PANE_ID`, `TAB_ID`, `WORKSPACE_ID`, `SESSION`) include no client id. The focused workspace is therefore derived from a `focus.state` file written by the host-side focus watcher goroutine (and supplemented by the `workspace.focused` plugin hook for API-driven focus changes) and read by `clientagent.Tick` over ssh — not from a per-client API. This is accurate only when the remote client is the **sole interactive client** on the herdr session; when a host TUI and a remote client coexist, the host session's last-write wins.
- **Verification**: automated — `TestClientAgentFocusScoping` in `internal/clientagent` exercises all three assertions. Conditional on D-8 operator ratification.
- **Criticality**: must
- **See also** [HPO-R-009](#hpo-r-009), [HPO-R-010](#hpo-r-010), [HPO-R-011](#hpo-r-011)
