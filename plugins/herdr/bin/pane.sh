#!/bin/sh
# pane.sh <subcommand> [args] — called by herdr as the pane's argv.
# The shim is written by build.sh at install time (absolute path to nexus3 binary).
SHIM="$(dirname "$0")/../nexus3-shim.sh"

case "$1" in
    attach|workspaces)
        # Long-lived panes: exec directly. Exit means the pane is done.
        exec "$SHIM" herdr "$@"
        ;;
    shell)
        # Guest interactive shell: exec into the sandbox identified by NEXUS3_WORKSPACE.
        # Herdr provides a PTY for this pane, so exec works as an interactive shell.
        REF="${NEXUS3_WORKSPACE:-}"
        if [ -z "$REF" ]; then
            echo "pane.sh shell: NEXUS3_WORKSPACE not set" >&2
            exit 1
        fi
        # Resolve workspace guest directory (prints /root when no workspace is mounted).
        SHELL_CWD="$("$SHIM" herdr shell-cwd "$REF" 2>/dev/null)"
        if [ -z "$SHELL_CWD" ]; then
            SHELL_CWD="/root"
        fi
        # Prefer bash as a login shell; fall back to /bin/sh for minimal images.
        # The probe must run in the GUEST. Testing the host for /usr/bin/bash
        # asked the wrong machine entirely: on macOS (a supported platform,
        # where bash lives at /bin/bash) every guest would be demoted to
        # /bin/sh, and on a guest with no bash the exec would fail and the pane
        # would close before the error could be read.
        GUEST_SHELL=$("$SHIM" exec "$REF" /bin/sh -c 'command -v bash 2>/dev/null || echo /bin/sh' 2>/dev/null | tr -d '\r' | tail -n 1)
        if [ -z "$GUEST_SHELL" ]; then
            GUEST_SHELL=/bin/sh
        fi
        case "$GUEST_SHELL" in
            */bash) exec "$SHIM" exec --pty --cwd "$SHELL_CWD" "$REF" "$GUEST_SHELL" -l ;;
            *)      exec "$SHIM" exec --pty --cwd "$SHELL_CWD" "$REF" "$GUEST_SHELL" ;;
        esac
        ;;
    create-space)
        # Discoverable create+boot+space action. Maps to herdr create-from-file subcommand.
        "$SHIM" herdr create-from-file
        STATUS=$?
        printf "Command exited with status %d. Press Enter to close.\n" "$STATUS"
        read -r _
        exit "$STATUS"
        ;;
    space-agent)
        # Launch a Claude agent in an existing sandbox. Prompts for sandbox ref and
        # slice brief on stdin, then drives claude via herdr pane commands.
        "$SHIM" herdr agent-from-file
        STATUS=$?
        printf "Command exited with status %d. Press Enter to close.\n" "$STATUS"
        read -r _
        exit "$STATUS"
        ;;
    create|logs|doctor|launch)
        # Short-lived panes: run and then pause so errors stay visible.
        "$SHIM" herdr "$@"
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
