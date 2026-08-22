# herdr ↔ nexus3 integration

herdr is a terminal workspace manager. nexus3 ships a native herdr plugin
(`plugins/herdr/`) that maps each nexus3 sandbox to a herdr workspace whose
panes are guest shells running inside the VM.  This document is the canonical
reference for that integration.

---

## The space == sandbox model

Every nexus3 sandbox gets exactly one herdr workspace (a "space").  The
mapping is 1:1 and enforced by a binding store
(`<storeRoot>/herdr-space-bindings.json`; see `internal/cli/cmd_herdr_space.go`).

**Process transparency (crabbox parity).** Transparency is achieved by running
_guest shells_ as herdr pane processes — not by mounting the guest filesystem
on the host.  Each pane executes:

```
nexus3 exec <sandbox-ref> /bin/sh
```

From the user's perspective, every pane in the workspace _is_ the sandbox
shell, indistinguishable from a native crabbox terminal session.  The herdr
workspace is purely a UI grouping container; it does not back, mount, or proxy
any filesystem.

**The space label** follows the convention `nexus3:<sandbox-handle>`, e.g.
`nexus3:demo-orca-01`.  The label is set at creation time and is stable for
the lifetime of the sandbox.

---

## Creating a space for a sandbox

Creating a space is a two-step operation. nexus3 never closes a tab or pane it
did not itself just open — see "Why nexus3 never closes a tab" below — so the
guest pane is **grafted onto** the root pane herdr creates, rather than opened
as a second tab that something would then have to clean up.

### Step 1 — Create the herdr workspace

```sh
WS_JSON=$(herdr workspace create --label 'nexus3:<sandbox-handle>' --no-focus --cwd '<sandbox-host-mount-path>')
WS_ID=$(echo "$WS_JSON" | python3 -c \
  "import sys,json; d=json.load(sys.stdin); print(d['result']['workspace']['workspace_id'])")
ROOT_PANE_ID=$(echo "$WS_JSON" | python3 -c \
  "import sys,json; d=json.load(sys.stdin); print(d['result']['root_pane']['pane_id'])")
```

`--no-focus` keeps the current workspace active while the new one is created in
the background.  `workspace_id` is an opaque short ID (e.g. `wB`) returned by
herdr.

`--cwd` is the sandbox's own host-side mount path (the `HostPath` of its first
live mount), passed whenever one is known. Without it, `herdr workspace
create` falls back to the cwd of whichever workspace happens to be focused on
the host — which is unrelated to this sandbox and, in a multi-project setup,
is very often the wrong repository entirely. When no host path is known (the
sandbox has no live mount), `--cwd` is omitted so herdr applies its own
default rather than passing an empty flag. `--cwd` still matters even though
the guest pane below shares the root tab rather than opening its own: it sets
the root pane's own directory, so the host shell sitting beside the guest
shell lands in the project too.

`herdr workspace create` always materialises a root tab and a root pane in the
new workspace — `ROOT_PANE_ID` above is that pane's id, captured from
`result.root_pane.pane_id`. Step 2 splits the guest pane onto it.

The binding store is updated with `HerdrSpaceBinding{SpaceLabel, HerdrWorkspaceID, SandboxHandle, SandboxID}` to record the 1:1 relationship.

### Step 2 — Graft the guest-shell pane onto the root pane

```sh
herdr plugin pane open \
  --plugin nexus3 \
  --entrypoint shell \
  --placement split \
  --target-pane "$ROOT_PANE_ID" \
  --direction right \
  --env NEXUS3_WORKSPACE=<sandbox-ref> \
  --focus
```

This splits the guest shell into the workspace's existing tab, beside the root
pane, instead of opening a second tab. Split and zoomed placements reject
`--workspace` unconditionally — not because of `--target-pane`, but because
those placements target an existing pane and the server refuses `--workspace`
regardless of what other flags are present:

```json
{"error":{"code":"invalid_params","message":"split and zoomed plugin panes target an existing pane; use target_pane_id"}}
```

**Fallback.** When no root pane id could be captured — herdr answered in
plain-text mode rather than JSON, or an existing workspace is being reused
rather than freshly created (its root pane id from creation time is not
retained) — the pane is opened with `--workspace "$WS_ID"` instead, landing in
a second tab. That is today's pre-graft behaviour and a degradation, not a
failure: nexus3 still never closes anything to compensate for it.

```sh
herdr plugin pane open \
  --plugin nexus3 \
  --entrypoint shell \
  --workspace "$WS_ID" \
  --env NEXUS3_WORKSPACE=<sandbox-ref> \
  --focus
```

### Why nexus3 never closes a tab

An earlier version of this integration opened the guest pane as a second tab
and then closed the root tab herdr had just created. Live testing surfaced two
problems with that: `tab close` SIGHUPs the pane being closed (observed:
`ExitStatus { code: 1, signal: Some("Hangup") }`) — if the root tab was not
actually a stray, whatever was running in it dies — and closing the workspace's
last tab can destroy the workspace outright, losing the cwd the whole point
was to set correctly.

The reference integration for herdr (crabbox) has an explicit written policy:
"Herdr owns panes and actions; Crabbox owns credentials and lifecycle." /
"The host owns installation and UI." Crabbox has zero `tab close` / `pane
close` / `workspace close` calls anywhere — it only ever adds panes, using
`--target-pane`. nexus3 adopts the same stance: it grafts, it never destroys a
terminal it did not create.

---

## Opening additional guest-shell panes

Additional guest-shell panes can be opened at any time via the `shell` action.

### Via CLI

```sh
herdr plugin pane open \
  --plugin nexus3 \
  --entrypoint shell \
  --workspace "$WS_ID" \
  --env NEXUS3_WORKSPACE=<sandbox-ref>
```

### Via herdr keybinding

Add to `~/.config/herdr/config.toml` (or your project herdr config):

```toml
[[keys.command]]
key   = "prefix+shift+n"          # choose any free binding
type  = "plugin_action"
command = "nexus3:shell"           # action id from herdr-plugin.toml [[actions]] id
```

Then reload the running server:

```sh
herdr server reload-config
```

> **Note on action IDs.** The `[[actions]]` table in `plugins/herdr/herdr-plugin.toml`
> currently defines `attach`, `create`, `logs`, and `doctor`.  The `shell` pane
> is opened via `--entrypoint shell` rather than a named action; if a future
> slice (SF2) promotes `shell` to a top-level action, update the `command` value
> above to match the new action id (format: `nexus3:<action-id>`).

---

## Server-side-only deployment

The nexus3 herdr plugin is registered and executed **on the engine host** (e.g.
`engine-03`).  A remote operator or minion machine needs nothing installed:

```sh
# on the minion — connect to the engine's herdr server
herdr --remote engine-03 workspace list
herdr --remote engine-03 plugin pane open --plugin nexus3 --entrypoint shell \
  --workspace "$WS_ID" --env NEXUS3_WORKSPACE=<sandbox-ref>
```

Plugin resolution, pane process spawning, and all `nexus3 exec` calls happen
server-side.  The `--remote` flag is a pure transport; no nexus3 binary, no
herdr plugin installation, and no herdr config is required on the client.

---

## Sandbox lifecycle → space lifecycle

| Sandbox event | Space action |
|---------------|--------------|
| `nexus3 sandbox create` | `herdr workspace create --label nexus3:<handle> --cwd <host-mount-path>`, then pane open grafted onto the root pane (`--target-pane`); nothing is ever closed |
| `nexus3 sandbox pause`  | Panes become idle (exec returns); workspace persists |
| `nexus3 sandbox resume` | Re-open panes or reconnect existing ones |
| `nexus3 sandbox remove` | `herdr workspace close <ws-id>` (binding removed from store) |

---

## Non-goals

The following are explicitly **out of scope** for this integration:

- **No filesystem backing.** herdr workspaces do not mount or proxy the guest
  filesystem.  There is no FUSE layer, no 9p mount, and no virtio-fs share
  driven by herdr.
- **No herdr fork or patch.** The integration uses the published herdr plugin
  API (`[[build]]`, `[[panes]]`, `[[actions]]` in `herdr-plugin.toml`) only.
  No fork, vendor patch, or private herdr build is required or planned.
- **No `[[workspace_provider]]` or other contribution tables.** The plugin
  manifest contains only `[[build]]`, `[[panes]]`, and `[[actions]]`.  herdr's
  workspace-provider extension point is not used.
- **`identity_cwd` stays local.** herdr's `identity_cwd` (the directory used
  to identify which workspace to focus) remains the local host path.  The guest
  filesystem layout does not influence it.
- **No filesystem-backed workspace adoption.** The plugin always creates a new
  workspace per sandbox (`CREATE` semantics); it does not adopt or decorate an
  existing focused workspace.

---

## Known limitations

- **New tabs opened by the user launch a HOST shell, not a guest shell.**
  When the user opens a new tab in a nexus3 space directly through herdr
  (rather than via the `shell` action / `space-open-pane`), herdr has no way
  to know that tab should run `nexus3 exec <sandbox-ref> /bin/sh` instead of
  a plain host shell. This is **not fixable in nexus3**: the herdr plugin ABI
  has no per-workspace default entrypoint. `entrypoint` exists only on
  `PluginPaneOpenParams` — a per-open argument supplied by whoever opens the
  pane — and is absent from both `WorkspaceCreateParams` and
  `TabCreateParams`, so nothing in the workspace itself can carry a default
  that a new-tab keybinding would pick up. Fixing this requires an upstream
  herdr feature (a per-workspace default entrypoint, or an equivalent ABI
  addition); nexus3 cannot work around it from its side of the plugin
  boundary.
- **New tabs still do not open in the project directory, even with `--cwd`
  set at creation.** A newly created tab inherits the currently *focused*
  pane's directory, not the workspace's or root pane's. The nexus3 guest
  pane's own cwd, as far as herdr can observe it, is the plugin process's own
  directory — not the sandbox's guest working tree — so focusing the guest
  pane and opening a new tab does not land it in the project either. This is a
  known gap, not something this integration fixes; there is no nexus3-side
  workaround identified for it yet.

---

## Plugin manifest reference

`plugins/herdr/herdr-plugin.toml` declares:

| Table | IDs |
|-------|-----|
| `[[build]]` | _(one entry; runs `build.sh`)_ |
| `[[panes]]` | `attach`, `shell`, `workspaces`, `create`, `logs`, `doctor`, `launch` |
| `[[actions]]` | `attach`, `create`, `logs`, `doctor` |

No other contribution tables exist in the manifest.
