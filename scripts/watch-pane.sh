#!/bin/sh
# watch-pane.sh <pane-id> — block until the in-guest agent in <pane-id> STOPS,
# print why, and exit with a code that names the reason.
#
# Run it as a background task in the same turn you dispatch, and let the harness
# re-invoke you when it exits:
#
#     scripts/watch-pane.sh w7M:p2          # run_in_background: true
#
# Exit codes (all non-zero: this script never exits 0 on a stop, because an
# exit-0 background task reads as "nothing to see"):
#
#     10  AGENT_IDLE           — stopped, no question detected
#     11  AGENT_QUESTION       — stopped on a prompt awaiting a reply
#     12  AGENT_NEVER_STARTED  — never became working inside the start grace
#      2  usage error
#      3  REFUSED — the pane could not be read, so no verdict is possible
#
# ---------------------------------------------------------------------------
# THE DETECTION RULE
# ---------------------------------------------------------------------------
#
# Implemented from .claude/skills/nexus3-slice-sandbox/SKILL.md section 3b,
# which is the authoritative statement of it. In brief, and in the order the
# rules actually bind:
#
#   * MOVEMENT DECIDES. A working agent repaints — the spinner animates and its
#     elapsed timer ticks every second — so its pane text CHANGES between two
#     reads a few seconds apart. A stopped agent renders a static pane. That
#     survives the next layout change; a marker string does not. Four false
#     stops in one session were each "fixed" by a pattern the next layout
#     defeated.
#
#   * The interrupt affordance is a FAST PATH TO WORKING ONLY. Its presence
#     confirms; its ABSENCE proves nothing. An agent blocked on its own
#     background subagent is working and has no interrupt affordance at all.
#
#   * ONE definition of "working", called by every loop INCLUDING the start-grace
#     loop. Two copies drift the moment one learns something — that is bug 3 in
#     the skill's own list. is_working() below is that one definition and there
#     is no second.
#
#   * Strings are the QUESTION discriminator, applied ONLY after the pane has
#     settled. Movement cannot tell a question from a stop, since both are
#     static. Nothing in classify_question() may ever be consulted to decide
#     whether the agent is working.
#
#   * AMBIGUITY BIASES TO WORKING. A false stop sends the orchestrator to review
#     work that does not exist yet; a missed stop only costs waiting.
#
# A FIFTH false-stop mode, not in the skill's list at the time this was written:
#
#   * A SCROLLED-UP PANE puts the ticking spinner outside the read window, so a
#     busy agent reads as static and stops being distinguishable from an idle
#     one. `herdr pane get <pane-id>` reports scroll.offset_from_bottom
#     directly. An INDETERMINATE scroll answer — herdr unreachable, field
#     absent, non-numeric — reads as SCROLLED, i.e. biases to WORKING.
#
#     Do NOT reach for `herdr workspace list` here: it reports only each
#     workspace's ROOT pane, so it cannot see a guest pane like w7M:p2 at all.
#
# And one read trap that reads as a stop if you fall through it:
#
#   * `herdr pane read --source recent-unwrapped` returns EMPTY on a pane that
#     has not scrolled yet — the normal state of a freshly dispatched agent.
#     Empty compares equal to empty, so "no movement", so "stopped". Fall back
#     to `--source visible`. If BOTH are empty the pane is unreadable and this
#     script REFUSES (exit 3) rather than calling it a stop.
#
# ---------------------------------------------------------------------------
# TESTING
# ---------------------------------------------------------------------------
#
# Every herdr call goes through the HERDR_BIN seam so the logic can be driven
# hermetically against recorded transcripts with no herdr and no VM. See
# internal/cli/watch_pane_test.go, which drives a known-WORKING transcript pair
# and a known-IDLE transcript pair through this file.
#
# Note the trap the skill names: "always WORKING" passes every positive test
# there is. The negative test — TestWatchPane_IdleTranscriptExitsIdle — is the
# one that bites, because a stuck-on-WORKING implementation never exits and
# fails on the timeout.

set -u

PANE="${1:-}"
if [ -z "$PANE" ]; then
    echo "usage: watch-pane.sh <pane-id>" >&2
    exit 2
fi

# --- seam -------------------------------------------------------------------
HERDR_BIN="${HERDR_BIN:-herdr}"

# --- tunables ---------------------------------------------------------------
# Overridable so the hermetic tests run in seconds instead of minutes. The
# defaults are reasoned, not measured against a live guest — this script cannot
# be run against one from inside a sandbox:
#
#   MOVEMENT_GAP=3   claude's spinner timer ticks once a second, so a 3 s gap
#                    spans ~3 repaints. One second would be a coin flip against
#                    a read that lands on the same frame.
#   POLL_INTERVAL=10 the cost of a late-noticed stop is 10 s of waiting; the
#                    cost of polling harder is a `herdr pane read` per pane per
#                    interval across every watched slice.
#   START_GRACE=180  `nexus3 herdr agent` alone allows 90 s just to reach the
#                    claude prompt, and the agent then has to read its brief
#                    before it repaints. 180 s is that with headroom.
#   SETTLE_ROUNDS=2  extra not-working rounds required before declaring a stop.
#                    Turns a single unlucky sample into a delay, not a verdict.
MOVEMENT_GAP="${WATCH_PANE_MOVEMENT_GAP:-3}"
POLL_INTERVAL="${WATCH_PANE_POLL_INTERVAL:-10}"
START_GRACE="${WATCH_PANE_START_GRACE:-180}"
SETTLE_ROUNDS="${WATCH_PANE_SETTLE_ROUNDS:-2}"

# read_pane — echo the pane's text. Returns non-zero when the pane cannot be
# read at all, which callers must treat as a REFUSAL and never as a stop.
#
# recent-unwrapped first (sharper, no wrap artefacts), visible as the fallback
# because recent-unwrapped is empty until the pane has scrolled.
read_pane() {
    _out=$("$HERDR_BIN" pane read "$PANE" --source recent-unwrapped 2>/dev/null)
    if [ -n "$(printf '%s' "$_out" | tr -d '[:space:]')" ]; then
        printf '%s' "$_out"
        return 0
    fi
    _out=$("$HERDR_BIN" pane read "$PANE" --source visible 2>/dev/null)
    if [ -n "$(printf '%s' "$_out" | tr -d '[:space:]')" ]; then
        printf '%s' "$_out"
        return 0
    fi
    return 1
}

# pane_scrolled — 0 (true) when the pane is scrolled up OR the answer is
# indeterminate; 1 (false) only on a definite "sitting at the bottom".
#
# The asymmetry is the point. A scrolled pane hides the spinner, so a busy agent
# reads as static; not knowing whether it is scrolled is exactly as dangerous as
# knowing that it is. Both bias to WORKING.
pane_scrolled() {
    _get=$("$HERDR_BIN" pane get "$PANE" 2>/dev/null) || return 0
    # Pull offset_from_bottom out of the JSON without needing jq — jq is not
    # guaranteed on the host and an absent jq must not silently mean "at bottom".
    _off=$(printf '%s' "$_get" \
        | tr ',{}' '\n\n\n' \
        | grep -o '"offset_from_bottom"[[:space:]]*:[[:space:]]*[0-9-]*' \
        | head -n 1 \
        | grep -o '[0-9-]*$')
    case "${_off:-}" in
        '' | *[!0-9]*) return 0 ;;   # absent, negative, or non-numeric → indeterminate → SCROLLED
    esac
    [ "$_off" -gt 0 ]
}

# working_marker — the interrupt/background affordances. FAST PATH ONLY.
#
# Presence means working. Absence means NOTHING: rule 2 of the skill's list is
# an agent blocked on its own subagent, which is working and permanently shows
# none of these. Never invert this into an idle test.
working_marker() {
    printf '%s' "$1" | grep -qE 'esc to interrupt|ctrl\+b to run in background|to run in background'
}

# ===========================================================================
# is_working — THE ONE DEFINITION. Every loop in this file calls this and no
# loop reimplements any part of it. Adding a second copy is bug 3 in the
# skill's list of false stops, and it is a bug even when the copy is correct
# today.
#
# Returns 0 = WORKING, 1 = NOT WORKING, 3 = UNREADABLE (refuse; not a stop).
#
# Precedence:
#   1. unreadable            → 3, refuse
#   2. scrolled/indeterminate→ WORKING   (spinner is outside the read window)
#   3. text changed          → WORKING   (movement decides)
#   4. interrupt affordance  → WORKING   (fast path)
#   5. otherwise             → NOT WORKING
# ===========================================================================
is_working() {
    _a=$(read_pane) || return 3

    if pane_scrolled; then
        return 0
    fi

    sleep "$MOVEMENT_GAP"

    _b=$(read_pane) || return 3

    if [ "$_a" != "$_b" ]; then
        return 0
    fi
    if working_marker "$_b"; then
        return 0
    fi
    return 1
}

# classify_question — the QUESTION discriminator. Called ONLY on a pane that
# is_working has already declared settled, and consulted by nothing else.
#
# Movement cannot separate "stopped on a question" from "stopped, done": both
# are static. Strings are all there is here, and unlike the working check a
# wrong answer is cheap — it mislabels one already-real stop, it cannot invent
# a stop that did not happen.
#
# These patterns are claude's approval and menu surfaces. They are deliberately
# broad: over-reporting QUESTION when the agent merely finished is a worse
# message, not a worse outcome, since the orchestrator looks at the pane either
# way.
classify_question() {
    printf '%s' "$1" | grep -qE \
        'Do you want|Would you like|\(y/n\)|❯ *1\.|^ *1\. Yes|Enter to confirm|to confirm.*esc to'
}

emit() { printf 'watch-pane[%s]: %s\n' "$PANE" "$1"; }

# --- start grace ------------------------------------------------------------
# Wait for the agent to become WORKING at least once. Until it has, "not
# working" means "has not started yet", not "has stopped" — reporting AGENT_IDLE
# here would send the orchestrator to review a slice that never began.
#
# This loop calls is_working. It does not contain a check of its own.
#
# The deadline is read off the WALL CLOCK, not accumulated from MOVEMENT_GAP and
# POLL_INTERVAL. Adding the tunables assumes the reads themselves are free, and
# at the limit — both tunables zero, which is how the hermetic tests run the
# script — the accumulator never advances at all and the grace loop spins
# forever. A grace period is an amount of TIME; ask the clock what time it is.
_deadline=$(( $(date +%s) + START_GRACE ))
_started=0
while [ "$(date +%s)" -lt "$_deadline" ]; do
    is_working
    _rc=$?
    if [ "$_rc" -eq 0 ]; then
        _started=1
        break
    fi
    if [ "$_rc" -eq 3 ]; then
        emit "REFUSED: pane could not be read (both --source recent-unwrapped and --source visible were empty). No verdict is possible."
        exit 3
    fi
    sleep "$POLL_INTERVAL"
done

if [ "$_started" -eq 0 ]; then
    emit "AGENT_NEVER_STARTED: no working signal within ${START_GRACE}s. The brief may never have been submitted — check the input box: $HERDR_BIN pane read $PANE --source visible"
    exit 12
fi

emit "agent is working; watching for a stop"

# --- watch ------------------------------------------------------------------
# Loop until is_working reports NOT WORKING for SETTLE_ROUNDS consecutive
# rounds. One round is not enough: a read can land between repaints.
_settled=0
while :; do
    is_working
    _rc=$?
    case "$_rc" in
        0)
            _settled=0
            sleep "$POLL_INTERVAL"
            continue
            ;;
        3)
            emit "REFUSED: pane became unreadable while watching. Not reporting a stop, because an unreadable pane is not evidence of one."
            exit 3
            ;;
    esac

    _settled=$((_settled + 1))
    if [ "$_settled" -le "$SETTLE_ROUNDS" ]; then
        sleep "$POLL_INTERVAL"
        continue
    fi

    # Settled. NOW, and only now, strings get a say.
    _final=$(read_pane) || {
        emit "REFUSED: pane became unreadable at the moment of the stop."
        exit 3
    }
    if classify_question "$_final"; then
        emit "AGENT_QUESTION: stopped awaiting a reply. Answer through the pane: $HERDR_BIN pane send-keys $PANE <n>  |  $HERDR_BIN pane run $PANE '<text>'  — then RESTART this watcher."
        exit 11
    fi
    emit "AGENT_IDLE: stopped with no question detected. Read the pane: $HERDR_BIN pane read $PANE --source visible"
    exit 10
done
