# Delegate agent state: herdr-native detection for space-agent dispatch

## Problem

`nexus herdr agent` dispatch previously called `herdr pane report-agent --state working`
after launching the agent. This gave nexus lifecycle authority over the pane, which
prevented herdr's native detection from updating the agent state. The OQ-1 probe
(e377844) observed `status=working seq=2045` frozen for the full session — herdr never
saw the state transitions the agent actually went through.

A secondary defect: stale lifecycle claims left by older nexus binaries on already-open
panes had the same masking effect on re-dispatch.

## Decision

Remove the forced `report-agent --state working` call entirely. Add a `release-agent`
call before taking the ready baseline, so any stale claim from a prior binary returns
the pane to native detection before nexus observes it.

## Readiness wait (herdrWaitAgentReady)

The baseline `Observe` is taken **before** `herdrPaneRun` sends the launch command.
`Ready(ctx, pane, baseline, timeout)` requires `Seq > baseline.Seq`, so a pane that was
already idle before launch cannot satisfy it — avoiding the false-positive that caused
the original report-agent workaround. If herdr cannot detect the agent (Unknown) or
times out, the path falls back to the existing `herdrPaneWaitOutput` readyMatch check.

## Delivery confirmation (herdrConfirmDelivery)

After the initial paste+Enter, herdr state is checked first on each settle cycle:

| herdr state after settle | action |
|---|---|
| working or blocked | delivered — return nil |
| seq > preDelivery.Seq, not idle/unknown | delivered — return nil |
| unknown | run screen classifier (STRANDED / SUBMITTED / UNKNOWN) |
| idle, seq unchanged | run screen classifier |

The invariant: herdr state is re-checked before every retry Enter press. An Enter is
never sent once the state shows working/blocked or seq has advanced.

## Footer chip false-positive

`briefInputBoxContentOnly` returns only lines between the box borders (╭─ … ╰─ or
rule-pair). Footer chips (e.g. `[Pasted text #N]` in the status bar below ╰─) are
excluded, so STRANDED detection is never triggered by a chip that already accepted.
