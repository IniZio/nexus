#!/bin/sh
# pane.sh <subcommand> [args] — called by herdr as the pane's argv.
# The shim is written by build.sh at install time (absolute path to nexus3 binary).
SHIM="$(dirname "$0")/../nexus3-shim.sh"

case "$1" in
    attach|workspaces)
        # Long-lived panes: exec directly. Exit means the pane is done.
        exec "$SHIM" __herdr-plugin "$@"
        ;;
    shell)
        # Guest interactive shell: exec into the sandbox identified by NEXUS3_WORKSPACE.
        # Herdr provides a PTY for this pane, so exec works as an interactive shell.
        REF="${NEXUS3_WORKSPACE:-}"
        if [ -z "$REF" ]; then
            echo "pane.sh shell: NEXUS3_WORKSPACE not set" >&2
            exit 1
        fi
        exec "$SHIM" exec "$REF" /bin/sh
        ;;
    create-space)
        # Discoverable create+boot+space action. Maps to space-create-from-file subcommand.
        "$SHIM" __herdr-plugin space-create-from-file
        STATUS=$?
        printf "Command exited with status %d. Press Enter to close.\n" "$STATUS"
        read -r _
        exit "$STATUS"
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
