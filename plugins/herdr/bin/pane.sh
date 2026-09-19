#!/bin/sh
# pane.sh <subcommand> [args] — called by herdr as the pane's argv.
# The shim is written by build.sh at install time (absolute path to nexus binary).
SHIM="$(dirname "$0")/../nexus-shim.sh"

case "$1" in
    attach|workspaces)
        # Long-lived panes: exec directly. Exit means the pane is done.
        exec "$SHIM" herdr "$@"
        ;;
    shell)
        # Guest interactive shell: exec into the sandbox identified by NEXUS_WORKSPACE.
        # Herdr provides a PTY for this pane, so exec works as an interactive shell.
        REF="${NEXUS_WORKSPACE:-}"
        if [ -z "$REF" ]; then
            echo "pane.sh shell: NEXUS_WORKSPACE not set" >&2
            exit 1
        fi
        # Resolve workspace guest directory (prints /root when no workspace is mounted).
        # Distinguish command failure (stale binary) from a legitimate /root answer:
        # a zero-exit /root means no mount; a non-zero exit means the binary is broken.
        SHELL_CWD=$("$SHIM" herdr shell-cwd "$REF" 2>/dev/null)
        SHELL_CWD_STATUS=$?
        if [ "$SHELL_CWD_STATUS" -ne 0 ]; then
            printf "pane.sh: 'nexus herdr shell-cwd %s' failed (exit %d)\n" "$REF" "$SHELL_CWD_STATUS"
            printf "The nexus binary is likely stale and does not recognise the 'herdr' command group.\n"
            printf "Fix: reinstall the nexus binary and re-run plugins/herdr/build.sh\n"
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
        # Herdr detects agents from the pane's HOST foreground process. Here
        # that is `nexus exec`, never the claude running inside the VM, so the
        # pane read agent_status=unknown and the agent was missing from the
        # sidebar. HERDR_AGENT is herdr's documented hint for exactly this
        # (docs: "VMs and sandbox wrappers"): it names the screen manifest to
        # apply to this foreground process, and detection then runs on the
        # terminal buffer — which the guest's claude UI already paints. A bare
        # guest prompt classifies as idle (herdr's known-agent fallback).
        # Exported on the exec'd process only; not set globally.
        export HERDR_AGENT="${HERDR_AGENT:-claude}"
        # Prefer the installed guest-shell entry point (herdr's default_shell,
        # a symlink to nexus dispatched on argv[0]). It resolves the same
        # binding from HERDR_WORKSPACE_ID and, unlike a bare `nexus exec`,
        # runs the supervised shell whose last-pane reaper STOPS the sandbox
        # when the workspace closes. Without it a workspace holding only this
        # plugin pane left the VM running after close (2026-09-19). Fall back
        # to the raw exec when the entry point is not installed.
        NEXUS_BIN=$(sed -n 's/^exec "\(.*\)" "\$@"$/\1/p' "$SHIM" 2>/dev/null | head -n 1)
        GUEST_ENTRY="${NEXUS_BIN%/*}/nexus-guest-shell"
        if [ -n "${HERDR_WORKSPACE_ID:-}" ] && [ -x "$GUEST_ENTRY" ] && [ -f "$GUEST_ENTRY.nexusbin" ]; then
            exec "$GUEST_ENTRY"
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
        # NEXUS_WORKTREE_AUTO=1 selects --auto (the repo-level conditional rule:
        # bind when some sibling workspace in this repo is already nexus-bound
        # or the checkout carries .nexus/config.yaml / .nexus/Containerfile; skip only
        # when neither). The worktree.created event hook sets it; the explicit
        # "sandbox this worktree" action does not, because an operator who asked
        # for a sandbox by name has already made the decision the predicate exists
        # to make.
        set -- "$WS"
        if [ "${NEXUS_WORKTREE_AUTO:-}" = "1" ]; then
            set -- --auto "$WS"
        fi
        printf "nexus: provisioning a sandbox for this worktree (image pull + disk + VM boot; can take a few minutes on a cold cache)...\n\n"
        # Tee the build into the per-workspace provisioning log. Every other
        # pane of this workspace that opens before the sandbox is ready
        # (nexus-guest-shell, herdrWtCreateLogPath in Go — same formula)
        # tails this file instead of waiting in silence. Truncated per run;
        # herdr reuses workspace IDs. The exit status crosses the pipe via a
        # temp file because POSIX sh has no PIPESTATUS.
        CREATE_LOG_DIR="${XDG_STATE_HOME:-$HOME/.local/state}/nexus"
        CREATE_LOG="$CREATE_LOG_DIR/herdr-wt-create-ws-$WS.log"
        mkdir -p "$CREATE_LOG_DIR" 2>/dev/null
        : > "$CREATE_LOG" 2>/dev/null
        STATUS_FILE=$(mktemp)
        { "$SHIM" herdr worktree-sandbox "$@" 2>&1; echo "$?" > "$STATUS_FILE"; } | tee -a "$CREATE_LOG"
        STATUS=$(cat "$STATUS_FILE" 2>/dev/null)
        rm -f "$STATUS_FILE"
        [ -n "$STATUS" ] || STATUS=1
        if [ "$STATUS" -eq 0 ]; then
            exit 0
        fi
        # Non-zero: HOLD THE PANE OPEN.  This is the whole point of running the
        # build here.  Closing on failure is what buried the last one.
        printf "\nnexus: worktree-sandbox FAILED (exit %d). The error is above.\n" "$STATUS"
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
