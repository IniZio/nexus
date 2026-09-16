#!/bin/sh
# on-worktree-removed.sh — plugin event hook for worktree.removed
#
# herdr fires this hook when a worktree workspace is closed via
# `herdr worktree remove`.  It tears down ONLY the sandbox bound to that
# workspace: `nexus3 herdr prune --apply --workspace <id>` looks up the one
# binding whose herdr workspace id matches, reaps its VM, and deletes the
# binding.  Nothing else in the binding store is touched.
#
# It must NOT run the global `prune --apply`.  That verb walks every binding
# and reaps any whose workspace is absent from `herdr workspace list` — and a
# workspace whose tab the operator merely closed is absent from that list.
# Running it from this hook destroyed two unrelated live sandboxes (wG, w18)
# on 2026-09-16.  Global prune stays a manual verb; its dry form prints the
# candidates it would take.
#
# HERDR_PLUGIN_EVENT_JSON payload (worktree_removed, schema-verified):
#   {"type":"worktree_removed","workspace_id":"<id>","worktree":{...},"forced":<bool>}
# HERDR_WORKSPACE_ID is also injected by herdr for plugin event hooks.
# Resolution: prefer HERDR_WORKSPACE_ID; fall back to .workspace_id from the
# payload via jq.  With neither, log and exit 0 — never widen to a global
# prune.  A sandbox with NO binding row is not collectible here.
#
# OQ-1 (answered in session): worktree.removed fires ONLY when herdr drives
# the removal (herdr worktree remove).  A plain `git worktree remove` outside
# herdr does NOT fire this hook; a manual `nexus3 herdr prune` remains the
# backstop for that path.
SHIM="$(dirname "$0")/../nexus3-shim.sh"

WS="${HERDR_WORKSPACE_ID:-}"
if [ -z "$WS" ] && command -v jq >/dev/null 2>&1; then
    WS=$(printf '%s' "${HERDR_PLUGIN_EVENT_JSON:-}" \
        | jq -r '.workspace_id // empty' 2>/dev/null)
fi
if [ -z "$WS" ]; then
    echo "on-worktree-removed.sh: no workspace ID (HERDR_WORKSPACE_ID unset, jq fallback failed); refusing global prune" >&2
    exit 0
fi
"$SHIM" herdr focus-changed --workspace "$WS" --only-if-focused || true
exec "$SHIM" herdr prune --apply --workspace "$WS"
