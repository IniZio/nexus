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
# PANE-FIRST.  Open the provisioning pane and let the build run inside it,
# rather than running it here in the hook process where it has no surface.
#
# This is the path the defect was reported on: herdr fires worktree.created, the
# hook builds a VM for minutes, and until it finishes the operator's brand-new
# worktree workspace shows only a host-path shell with no indication that
# anything is happening.  If the build then fails, the error goes to the plugin
# log and nowhere else.  Opening the pane first fixes both halves: the build is
# visible while it runs, and pane.sh holds the pane open on failure.
#
# NEXUS3_WORKTREE_AUTO=1 carries the --auto predicate through to pane.sh, which
# is what keeps this hook conditional (bind only when a sibling workspace in the
# same repo is already nexus3-bound).
#
# Fail-open is preserved: if the pane cannot be opened we fall back to the old
# inline run rather than leaving the worktree unprovisioned.  A missing pane is
# a missing SIGNAL, not a reason to skip the work.
HERDR="${HERDR_BIN_PATH:-herdr}"
if "$HERDR" plugin pane open \
    --plugin nexus3 \
    --entrypoint worktree-sandbox \
    --placement tab \
    --no-focus \
    --workspace "$WS" \
    --env "NEXUS3_WORKTREE_AUTO=1"; then
    exit 0
fi
echo "on-worktree-created.sh: could not open the provisioning pane; provisioning inline (no progress will be visible)" >&2
exec "$SHIM" herdr worktree-sandbox --auto "$WS"
