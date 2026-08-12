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

Creating a space is a two-step operation:

### Step 1 — Create the herdr workspace

```sh
WS_JSON=$(herdr workspace create --label 'nexus3:<sandbox-handle>' --no-focus)
WS_ID=$(echo "$WS_JSON" | python3 -c \
  "import sys,json; d=json.load(sys.stdin); print(d['result']['workspace']['workspace_id'])")
```

`--no-focus` keeps the current workspace active while the new one is created in
the background.  `workspace_id` is an opaque short ID (e.g. `wB`) returned by
herdr.

The binding store is updated with `HerdrSpaceBinding{SpaceLabel, HerdrWorkspaceID, SandboxHandle, SandboxID}` to record the 1:1 relationship.

### Step 2 — Open the initial guest-shell pane

```sh
herdr plugin pane open \
  --plugin nexus3 \
  --entrypoint shell \
  --workspace "$WS_ID" \
  --env NEXUS3_WORKSPACE=<sandbox-ref>
```

This places a `nexus3 guest shell` tab in the new workspace.  The pane runs
`nexus3 exec <sandbox-ref> /bin/sh` via `plugins/herdr/bin/pane.sh`.

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
| `nexus3 sandbox create` | `herdr workspace create --label nexus3:<handle>` then pane open |
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

## Plugin manifest reference

`plugins/herdr/herdr-plugin.toml` declares:

| Table | IDs |
|-------|-----|
| `[[build]]` | _(one entry; runs `build.sh`)_ |
| `[[panes]]` | `attach`, `shell`, `workspaces`, `create`, `logs`, `doctor`, `launch` |
| `[[actions]]` | `attach`, `create`, `logs`, `doctor` |

No other contribution tables exist in the manifest.
