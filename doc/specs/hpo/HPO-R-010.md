---
id: HPO-R-010
type: requirement
concept: C-HPO
criticality: must
verification: automated
status: active
origin_decision_ref: herdr-plugin-ootb#D-10
summary: "Client restart cancels previous-process forwards; duplicate tcp/tcp6 rows collapse to one entry."
---

## HPO-R-010 — Forwards cancelled on client restart; duplicate rows collapsed {#hpo-r-010}

**When** `nexus-client` starts, it **shall** cancel any forwards that were applied by a previous client process and are no longer present in the current reconciliation set before applying new forwards; and **when** the host port-forward state file contains duplicate rows for the same sandbox port from both `tcp` and `tcp6` protocol entries, `nexus-client` **shall** collapse them to a single forward entry.

- **Why** — without cancellation on restart, forwards from a prior client process accumulate across restarts and are never torn down, causing orphaned listeners (observed: master pid 40518 older than client pid 45721 with orphan forward 15173); without deduplication, the duplicate tcp/tcp6 rows cause redundant reconcile attempts that may conflict with each other.
- **Fit criterion** — a test starts a fake client process that applies a set of forwards, then starts a second fake client process against the same state file and asserts: (a) the second process cancels the forwards applied by the first within its first reconcile tick; (b) a state file with duplicate tcp and tcp6 rows for the same port results in exactly one applied forward entry.
- **Verification**: automated — `TestClientAgentRestartCancelsOrphans` and `TestPortForwardDedup` in `internal/clientagent` and `internal/core/portfwd` respectively.
- **Criticality**: must
- **See also** [HPO-R-008](#hpo-r-008), [HPO-R-009](#hpo-r-009)
