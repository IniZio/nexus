#!/bin/sh
# on-worktree-created.sh — plugin event hook for worktree.created
#
# herdr fires this hook when a worktree workspace is opened.  It auto-provisions
# a sandbox for the new worktree workspace when the source workspace is already
# nexus3-bound (the --auto conditional rule in herdrWorktreeSandbox).
#
# HERDR_PLUGIN_EVENT_JSON payload (worktree_created, schema-verified):
#   {"type":"worktree_created","workspace":{"workspace_id":"<id>",...},"worktree":{...}}
# HERDR_WORKSPACE_ID is also injected by herdr for plugin event hooks.
#
# Workspace ID resolution: prefer HERDR_WORKSPACE_ID (injected by herdr);
# fall back to parsing workspace.workspace_id from HERDR_PLUGIN_EVENT_JSON via
# jq when HERDR_WORKSPACE_ID is absent.  If neither is available, log and exit 0
# (fail-open: never block herdr on a missing optional ID).
SHIM="$(dirname "$0")/../nexus3-shim.sh"

WS="${HERDR_WORKSPACE_ID:-}"
if [ -z "$WS" ] && command -v jq >/dev/null 2>&1; then
    WS=$(printf '%s' "${HERDR_PLUGIN_EVENT_JSON:-}" \
        | jq -r '.workspace.workspace_id // empty' 2>/dev/null)
fi
if [ -z "$WS" ]; then
    echo "on-worktree-created.sh: no workspace ID (HERDR_WORKSPACE_ID unset, jq fallback failed)" >&2
    exit 0
fi
exec "$SHIM" herdr worktree-sandbox --auto "$WS"
