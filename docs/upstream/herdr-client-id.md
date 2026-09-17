# Upstream request: client identity in herdr focus events and snapshot

**Project:** herdr  
**Protocol version probed:** 0.9.0 / protocol 22  
**Date:** 2026-09-16  
**Filed by:** nexus3 maintainers

## Background

nexus3 runs a remote-client daemon (`nexus3-client`) on macOS that forwards sandbox TCP ports scoped to the focused herdr workspace. To determine which workspace is focused, the daemon currently reads a `focus.state` file written by the host-side plugin on each `workspace.focused` event.

This design is a workaround for the absence of client identity in the herdr protocol. It is accurate only when the remote client is the **sole interactive client** on the session; with two clients attached the host session's last focus write wins, and the remote client cannot know whether a focus event originated from itself or from the host TUI.

## Evidence (read-only probes, 2026-09-16)

- `workspace_focused` event payload: `{type, workspace_id}` — no `client_id` field.
- `api snapshot` response: single session-global `focused_workspace_id` — no per-client map.
- Pane environment variables delivered to plugin hooks: `HERDR_PANE_ID`, `TAB_ID`, `WORKSPACE_ID`, `SESSION` — no `HERDR_CLIENT_ID`.
- Mac remote-client local state: `~/.local/state/herdr/client/endpoint-selection.json`, `endpoints.json` — no focus tracking; the client writes its focus into the host session via the remote-client-bridge (last-write-wins across clients).
- Third-party plugins reviewed: `herdr-fwd`, `herdr-machine-manager`, `herdr-cursor-open` — none observe per-client focus; `herdr-fwd/docs/limitations.md:9` explicitly documents this as a known limitation.

## Requested protocol additions

### 1. `client_id` on focus events

Add a `client_id` field to `workspace_focused`, `tab_focused`, and `pane_focused` events:

```json
{
  "type": "workspace_focused",
  "workspace_id": "ws-abc123",
  "client_id": "client-xyz789"
}
```

`client_id` should be stable for the lifetime of the client connection and opaque to plugins (a UUID or connection-scoped token is sufficient).

### 2. Per-client focus map in the `api snapshot`

Replace or augment the session-global `focused_workspace_id` with a per-client map:

```json
{
  "focused_workspace_id": "ws-abc123",
  "clients": [
    { "client_id": "client-xyz789", "focused_workspace_id": "ws-abc123" },
    { "client_id": "client-def456", "focused_workspace_id": "ws-other" }
  ]
}
```

This allows a plugin to resolve its own client's current focus at startup without waiting for a `workspace_focused` event.

### 3. `HERDR_CLIENT_ID` in plugin event and context env

Deliver `HERDR_CLIENT_ID` alongside the existing `HERDR_PANE_ID` / `TAB_ID` / `WORKSPACE_ID` / `SESSION` variables in plugin hook and event execution contexts. This lets a plugin filter events to its own client without parsing connection metadata.

## Why this matters

With these additions, nexus3-client can:

- Ignore `workspace_focused` events from other clients (host TUI, other remote sessions).
- Read its own focused workspace from `clients[self.client_id].focused_workspace_id` in the snapshot at startup rather than relying on a host-written file.
- Support multiple simultaneous remote clients without last-write-wins focus collisions.

Without them, the sole-interactive-client constraint is a permanent documentation caveat rather than a solvable problem.

## Known vendor gap: remote-client focus not reflected in snapshot/list/subscribe/hooks

herdr 0.9.0 logs workspace focus events from the Mac remote client to the session server log
(`~/.config/herdr/herdr-server.log` or `~/.config/herdr/sessions/<s>/herdr-server.log`),
but does NOT reflect them in `session.snapshot.focused_workspace_id`, `workspace.list`,
`events.subscribe` delivery, or any plugin hook. nexus3 works around this by tailing the
server log for lines matching `event="workspace.focus"` with `outcome="ok"` (see
`tailHerdrServerLog` in `internal/cli/cmd_herdr_focus_watch.go`). This gap should be
resolved upstream by making remote-client focus changes observable through the existing API
surfaces, eliminating the log-tail workaround.

## Known vendor gap: plugin event hooks fire only for API-driven focus

herdr 0.9.0 dispatches `[[events]]` hooks (including `workspace.focused`) only when a
workspace focus change originates from a programmatic API call (e.g. `workspace.focus`
JSON-RPC method). Focus changes driven by a user clicking in the Mac remote client UI or
switching workspaces in the host TUI update the session state — `workspace.list` and
`api snapshot` reflect the new `focused_workspace_id` immediately — but no plugin event
hook is invoked.

**Impact on nexus3**: the `[[events]] on = "workspace.focused"` hook in
`herdr-plugin.toml` is unreliable for tracking operator focus. nexus3 works around this
with a long-lived `runHerdrFocusWatch` goroutine (started in `herdrPluginLocalAgentStartup`)
that subscribes to the `events.subscribe` API stream — which DOES deliver all focus changes,
including client-UI-originated ones — and maintains `focus.state` directly.

**Filed**: this gap should be documented with the upstream herdr project; a plugin event
hook that fires for all focus-change sources (not just API calls) would let nexus3 remove
the watcher goroutine.
