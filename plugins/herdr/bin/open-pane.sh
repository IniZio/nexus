#!/bin/sh
# open-pane.sh <entrypoint> [placement] — called by herdr actions to open a pane.
ENTRYPOINT="$1"
PLACEMENT="${2:-tab}"
SHIM="$(dirname "$0")/../nexus3-shim.sh"

case "$ENTRYPOINT" in
    worktree-sandbox)
        # Bind the focused worktree workspace to a new nexus3 sandbox.
        #
        # PANE-FIRST: open the worktree-sandbox pane and let the provisioning run
        # INSIDE it (see pane.sh's worktree-sandbox case).  This used to run the
        # build inline in the action process, which meant two things, both bad:
        # the operator watched a minutes-long VM build with no output anywhere,
        # and a failure went only to `herdr plugin log list`.
        #
        # The pane is the surface the operator is already looking at, and pane.sh
        # holds it open on a non-zero exit so the error survives to be read.
        exec "$HERDR_BIN_PATH" plugin pane open \
            --plugin nexus3 \
            --entrypoint worktree-sandbox \
            --placement tab \
            --focus \
            --workspace "$HERDR_WORKSPACE_ID"
        ;;
    space-pause|space-resume|space-remove)
        # Lifecycle control on the sandbox bound to the focused herdr workspace.
        # These are not panes: they act and exit. Resolved by HERDR_WORKSPACE_ID
        # so no sandbox ref is required from the caller, exactly like
        # space-open-pane. A sandbox created outside herdr is adopted on the
        # spot rather than dead-ending on a missing binding.
        #
        # The entrypoint IDs keep the `space-` prefix (matching the herdr manifest)
        # but the `herdr` group drops it: space-pause → herdr pause, etc.
        VERB="${ENTRYPOINT#space-}"
        "$SHIM" herdr "$VERB" "$HERDR_WORKSPACE_ID"
        STATUS=$?
        if [ "$STATUS" -ne 0 ]; then
            echo "nexus3: $ENTRYPOINT failed (status $STATUS)" >&2
        fi
        exit "$STATUS"
        ;;
    space-open-pane)
        # Invoke nexus3 herdr space-open-pane to open an extra guest-shell pane
        # in the herdr workspace that is currently focused. Resolves the binding
        # by HERDR_WORKSPACE_ID so no sandbox ref is required from the caller.
        # space-open-pane keeps its space- prefix under the `herdr` group because
        # `herdr open-pane` is already taken by the non-space open-pane verb.
        exec "$SHIM" herdr space-open-pane "$HERDR_WORKSPACE_ID"
        ;;
    new-tab)
        # Context-aware new tab: opens a guest-shell pane when the focused
        # workspace is a nexus3 space, or falls through to herdr's built-in
        # tab-create otherwise. Safe to bind globally — non-nexus3 workspaces
        # (other repos, groundwork, …) get a normal host tab.
        exec "$SHIM" herdr new-tab "$HERDR_WORKSPACE_ID"
        ;;
    *)
        # Generic pane open: build optional --env arg only when NEXUS3_WORKSPACE is set.
        # Only tab carries --workspace. Every other placement targets an active or
        # existing pane and the server rejects --workspace for them:
        #   overlay/popup: "overlay and popup plugin panes target the active pane"
        #   split/zoomed:  "split and zoomed plugin panes target an existing pane;
        #                   use target_pane_id"
        case "$PLACEMENT" in
            overlay|popup|split|zoomed)
                if [ -n "$NEXUS3_WORKSPACE" ]; then
                    exec "$HERDR_BIN_PATH" plugin pane open \
                        --plugin nexus3 \
                        --entrypoint "$ENTRYPOINT" \
                        --placement "$PLACEMENT" \
                        --focus \
                        --env "NEXUS3_WORKSPACE=$NEXUS3_WORKSPACE"
                else
                    exec "$HERDR_BIN_PATH" plugin pane open \
                        --plugin nexus3 \
                        --entrypoint "$ENTRYPOINT" \
                        --placement "$PLACEMENT" \
                        --focus
                fi
                ;;
            *)
                if [ -n "$NEXUS3_WORKSPACE" ]; then
                    exec "$HERDR_BIN_PATH" plugin pane open \
                        --plugin nexus3 \
                        --entrypoint "$ENTRYPOINT" \
                        --placement "$PLACEMENT" \
                        --focus \
                        --workspace "$HERDR_WORKSPACE_ID" \
                        --env "NEXUS3_WORKSPACE=$NEXUS3_WORKSPACE"
                else
                    exec "$HERDR_BIN_PATH" plugin pane open \
                        --plugin nexus3 \
                        --entrypoint "$ENTRYPOINT" \
                        --placement "$PLACEMENT" \
                        --focus \
                        --workspace "$HERDR_WORKSPACE_ID"
                fi
                ;;
        esac
        ;;
esac
