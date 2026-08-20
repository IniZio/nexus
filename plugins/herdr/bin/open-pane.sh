#!/bin/sh
# open-pane.sh <entrypoint> [placement] — called by herdr actions to open a pane.
ENTRYPOINT="$1"
PLACEMENT="${2:-tab}"
SHIM="$(dirname "$0")/../nexus3-shim.sh"

case "$ENTRYPOINT" in
    space-pause|space-resume|space-remove)
        # Lifecycle control on the sandbox bound to the focused herdr workspace.
        # These are not panes: they act and exit. Resolved by HERDR_WORKSPACE_ID
        # so no sandbox ref is required from the caller, exactly like
        # space-open-pane. A sandbox created outside herdr is adopted on the
        # spot rather than dead-ending on a missing binding.
        "$SHIM" __herdr-plugin "$ENTRYPOINT" "$HERDR_WORKSPACE_ID"
        STATUS=$?
        if [ "$STATUS" -ne 0 ]; then
            echo "nexus3: $ENTRYPOINT failed (status $STATUS)" >&2
        fi
        exit "$STATUS"
        ;;
    space-open-pane)
        # Invoke the nexus3 space-open-pane subcommand to open an extra guest-shell
        # pane in the herdr workspace that is currently focused. Resolves the binding
        # by HERDR_WORKSPACE_ID so no sandbox ref is required from the caller.
        exec "$SHIM" __herdr-plugin space-open-pane "$HERDR_WORKSPACE_ID"
        ;;
    *)
        # Generic pane open: build optional --env arg only when NEXUS3_WORKSPACE is set.
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
