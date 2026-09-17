#!/bin/sh
# on-workspace-focused.sh — plugin event hook for workspace.focused
#
# herdr fires this hook when the operator switches focus to a workspace
# (herdr ≥0.9).  It updates the local focus.state file with the newly-focused
# workspace's sandbox binding so nexus clients know which sandbox has focus.
#
# HERDR_PLUGIN_EVENT_JSON payload (workspace_focused, schema-verified):
#   {"type":"workspace_focused","workspace_id":"<id>"}
# HERDR_WORKSPACE_ID is also injected by herdr for plugin event hooks.
# HERDR_SESSION carries the session identifier; the binary reads it from env.
#
# Workspace ID resolution: prefer HERDR_WORKSPACE_ID (injected by herdr);
# fall back to parsing workspace_id from HERDR_PLUGIN_EVENT_JSON via jq when
# HERDR_WORKSPACE_ID is absent.  If neither is available, log and exit 0
# (fail-open: never block herdr on a missing optional ID).
SHIM="$(dirname "$0")/../nexus-shim.sh"

WS="${HERDR_WORKSPACE_ID:-}"
if [ -z "$WS" ] && command -v jq >/dev/null 2>&1; then
    WS=$(printf '%s' "${HERDR_PLUGIN_EVENT_JSON:-}" \
        | jq -r '.workspace_id // empty' 2>/dev/null)
fi
if [ -z "$WS" ]; then
    echo "on-workspace-focused.sh: no workspace ID (HERDR_WORKSPACE_ID unset, jq fallback failed)" >&2
    exit 0
fi
exec "$SHIM" herdr focus-changed --workspace "$WS"
