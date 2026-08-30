#!/usr/bin/env bash
# Block until an in-guest agent stops needing nothing from you.
#
# herdr has no event bus and in-guest agents are not addressable by `herdr agent`
# verbs, so a dispatched agent that stopped for a reply is indistinguishable from
# one working hard — unless something watches. Run this as a background task in
# the same turn you dispatch, so the harness re-invokes you the moment the agent
# blocks or finishes.
#
# Usage: watch-pane.sh <pane-id> [poll-seconds] [start-grace-seconds]
#
# Exits printing one of:
#   AGENT_ASKED_QUESTION  — blocked on a menu or prompt; answer it, then RESTART
#                           this watcher, or the next question hangs silently
#   AGENT_IDLE            — finished, or died; read the tail and find out which
#   AGENT_NEVER_STARTED   — the prompt never took (queued behind other work, or
#                           a dead pane); NOT a completion, do not review a diff

set -uo pipefail

PANE="${1:?usage: watch-pane.sh <pane-id> [poll-seconds] [start-grace-seconds]}"
POLL="${2:-20}"
START_GRACE="${3:-30}"

# pane_text returns the pane's current rendered text.
#
# 24 lines, not 12: several working indicators (the background-agent roster, the
# spinner line) render in the body ABOVE the footer, so a short read cannot see
# them.
pane_text() {
  herdr pane read "$PANE" --source recent-unwrapped --lines 24 2>&1
}

# sample_state prints QUESTION, IDLE, or WORKING. It is the SOLE definition of
# those states; the start-grace loop and the steady-state loop both call it, so a
# new state is learned once rather than in two places that drift apart.
#
# THE PRIMARY SIGNAL IS MOVEMENT, NOT A STRING. Four separate false stops were
# hit on 2026-08-30 by asking "is <marker> present?", each from a different pane
# layout: a slash-command overlay repainting the footer; an agent blocked on its
# own subagent (footer loses "esc to interrupt" permanently); the start-grace
# loop holding a stale inlined copy of the check; and finally the roster
# switching from "Waiting for N background agents" to a live subagent row, which
# matched no marker at all. Each fix was correct and each was overtaken by the
# next layout — the approach was the defect, not any individual pattern.
#
# A working agent repaints: the spinner animates and its elapsed timer ticks
# every second, so the text CHANGES between two reads a few seconds apart. A
# genuinely stopped agent renders a static pane. Comparing two spaced reads
# therefore detects work regardless of which marker any given layout uses, and
# keeps working when the UI changes again.
#
# The string checks below are kept only as fast paths and as the QUESTION
# discriminator, which movement alone cannot distinguish from idle.
sample_state() {
  local a b
  a=$(pane_text)

  # Fast path: an explicit working marker needs no second read.
  if grep -q "esc to interrupt" <<<"$a" \
     || grep -qE "Waiting for [0-9]+ background agent" <<<"$a"; then
    echo WORKING
    return
  fi

  # No marker: decide by movement. MOVE_GAP must exceed the spinner's tick
  # (~1s) so a working pane is guaranteed to differ between the two reads.
  sleep "${MOVE_GAP:-3}"
  b=$(pane_text)
  if [[ "$a" != "$b" ]]; then
    echo WORKING
    return
  fi

  # Static pane: now the strings are meaningful.
  if grep -q "Enter to select" <<<"$b"; then
    echo QUESTION
  else
    echo IDLE
  fi
}

# Wait for the agent to actually START before watching for it to stop.
#
# Without this the watcher races its own dispatch: you send a prompt, the pane
# has not repainted yet, the first poll sees no "esc to interrupt" and reports
# AGENT_IDLE for work that never began. A false AGENT_IDLE is worse than no
# watcher at all — it reports finished work that does not exist, and you go
# review a commit the agent has not written.
#
# If the agent never starts within the grace window, say so rather than
# reporting idle: a prompt that was never delivered is a real failure mode
# (a queued `pane run`, a dead pane) and it must not read as "done".
# This MUST use sample_state, not its own copy of the working-check. An earlier
# revision inlined `grep -q "esc to interrupt"` here; when sample_state later
# learned that waiting-on-a-subagent is also WORKING, this loop kept the old
# narrow test and reported AGENT_NEVER_STARTED for an agent that had started
# and immediately delegated to a background agent (observed live 2026-08-30).
# One definition of "working", used by both loops.
for _ in $(seq "$START_GRACE"); do
  if [[ "$(sample_state)" == WORKING ]]; then
    started=1
    break
  fi
  sleep 1
done
if [[ -z "${started:-}" ]]; then
  echo "AGENT_NEVER_STARTED"
  echo "--- pane tail ---"
  herdr pane read "$PANE" --source recent-unwrapped --lines 40 2>&1 | tail -42
  exit 0
fi

# The pane footer reads "esc to interrupt" exactly while the agent is working, so
# its ABSENCE is the idle signal. That is why this is a poll loop and not
# `herdr pane wait-output`: a regex can match a string appearing, never one
# disappearing. Use wait-output instead when you know the marker you want.
#
# Both signals must hold for CONFIRM consecutive samples before they are
# believed. A single sample is not enough, because a transient full-width
# overlay repaints the footer: while the slash-command menu is open the working
# indicator is gone from the footer AND the menu itself renders "Enter to
# select". A one-shot read taken during that repaint reports a working agent as
# idle, or an autocomplete list as a question. Both were observed live —
# 2026-08-30, a watcher exited AGENT_IDLE while the agent was mid-tool-call with
# a slash menu on screen.
CONFIRM="${CONFIRM:-3}"
CONFIRM_GAP="${CONFIRM_GAP:-3}"

while true; do
  state=$(sample_state)

  if [[ "$state" != WORKING ]]; then
    # Re-sample before believing a stop. Any WORKING in the run means the pane
    # was mid-repaint, so discard and go back to polling.
    stable=1
    for _ in $(seq $((CONFIRM - 1))); do
      sleep "$CONFIRM_GAP"
      next=$(sample_state)
      if [[ "$next" == WORKING || "$next" != "$state" ]]; then
        stable=
        break
      fi
    done
    if [[ -n "$stable" ]]; then
      [[ "$state" == QUESTION ]] && echo "AGENT_ASKED_QUESTION" || echo "AGENT_IDLE"
      break
    fi
  fi

  sleep "$POLL"
done

echo "--- pane tail ---"
herdr pane read "$PANE" --source recent-unwrapped --lines 40 2>&1 | tail -42
