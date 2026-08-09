#!/bin/sh
# pane.sh <subcommand> [args] — called by herdr as the pane's argv.
# The shim is written by build.sh at install time (absolute path to nexus3 binary).
SHIM="$(dirname "$0")/../nexus3-shim.sh"

case "$1" in
    attach|workspaces)
        # Long-lived panes: exec directly. Exit means the pane is done.
        exec "$SHIM" __herdr-plugin "$@"
        ;;
    create|logs|doctor|launch)
        # Short-lived panes: run and then pause so errors stay visible.
        "$SHIM" __herdr-plugin "$@"
        STATUS=$?
        printf "Command exited with status %d. Press Enter to close.\n" "$STATUS"
        read -r _
        exit "$STATUS"
        ;;
    *)
        echo "pane.sh: unknown subcommand: $1" >&2
        exit 1
        ;;
esac
