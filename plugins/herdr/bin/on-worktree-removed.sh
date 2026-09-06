#!/bin/sh
# on-worktree-removed.sh — plugin event hook for worktree.removed
#
# herdr fires this hook when a worktree workspace is closed via
# `herdr worktree remove`.  It tears down the sandbox that was bound to that
# workspace by calling `nexus3 herdr prune --apply`, which walks all bindings,
# finds any whose herdr workspace is now gone, and removes their VMs.
#
# HERDR_PLUGIN_EVENT_JSON payload (worktree_removed, schema-verified):
#   {"type":"worktree_removed","workspace_id":"<id>","worktree":{...},"forced":<bool>}
# The workspace_id field identifies the closed workspace.  Resolution is
# through the existing binding store; a sandbox with NO binding row is not
# collectible here.  See ticket 18 AC-18h.
#
# OQ-1 (answered in session): worktree.removed fires ONLY when herdr drives
# the removal (herdr worktree remove).  A plain `git worktree remove` outside
# herdr does NOT fire this hook; space-prune remains the required backstop for
# that path.
SHIM="$(dirname "$0")/../nexus3-shim.sh"
exec "$SHIM" herdr prune --apply
