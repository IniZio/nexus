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
        # Distinguish command failure (stale binary) from a legitimate /root answer:
        # a zero-exit /root means no mount; a non-zero exit means the binary is broken.
        SHELL_CWD=$("$SHIM" herdr shell-cwd "$REF" 2>/dev/null)
        SHELL_CWD_STATUS=$?
        if [ "$SHELL_CWD_STATUS" -ne 0 ]; then
            printf "pane.sh: 'nexus3 herdr shell-cwd %s' failed (exit %d)\n" "$REF" "$SHELL_CWD_STATUS"
            printf "The nexus3 binary is likely stale and does not recognise the 'herdr' command group.\n"
            printf "Fix: reinstall the nexus3 binary and re-run plugins/herdr/build.sh\n"
            printf "Press Enter to close this pane.\n"
            read -r _
            exit 1
        fi
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
    worktree-sandbox)
        # Pane-FIRST provisioning.  The pane exists before the VM does, so the
        # build streams into a surface the operator is already looking at, and a
        # failure is legible where it happened instead of only in the plugin log.
        #
        # Resolved via HERDR_WORKSPACE_ID, exactly like the old inline action —
        # no sandbox ref is needed from the caller.
        WS="${HERDR_WORKSPACE_ID:-}"
        if [ -z "$WS" ]; then
            printf "pane.sh worktree-sandbox: HERDR_WORKSPACE_ID not set; cannot tell which worktree to sandbox.\n"
            printf "Press Enter to close this pane.\n"
            read -r _
            exit 1
        fi
        # NEXUS3_WORKTREE_AUTO=1 selects --auto (the repo-level conditional rule:
        # bind only when some sibling workspace in this repo is already
        # nexus3-bound). The worktree.created event hook sets it; the explicit
        # "sandbox this worktree" action does not, because an operator who asked
        # for a sandbox by name has already made the decision the predicate exists
        # to make.
        set -- "$WS"
        if [ "${NEXUS3_WORKTREE_AUTO:-}" = "1" ]; then
            set -- --auto "$WS"
        fi
        printf "nexus3: provisioning a sandbox for this worktree (image pull + disk + VM boot; can take a few minutes on a cold cache)...\n\n"
        "$SHIM" herdr worktree-sandbox "$@"
        STATUS=$?
        if [ "$STATUS" -eq 0 ]; then
            exit 0
        fi
        # Non-zero: HOLD THE PANE OPEN.  This is the whole point of running the
        # build here.  Closing on failure is what buried the last one.
        printf "\nnexus3: worktree-sandbox FAILED (exit %d). The error is above.\n" "$STATUS"
        printf "Press Enter to close this pane.\n"
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
