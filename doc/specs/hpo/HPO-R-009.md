---
id: HPO-R-009
type: requirement
concept: C-HPO
criticality: must
verification: automated
status: active
origin_decision_ref: herdr-plugin-ootb#D-9
summary: "A locally bound port is reported as a conflict and never recorded as an applied forward."
---

## HPO-R-009 — Locally bound port reported as conflict, never marked forwarded {#hpo-r-009}

**When** `Forwarder.Present` checks whether a port is forwarded and finds the local port already bound by a process other than the nexus forward, the system **shall** report the condition as a conflict and **shall not** record the port as applied in the `Manager`'s applied set.

- **Why** — the current `Forwarder.Present` implementation checks only whether the port is locally bound, without distinguishing nexus-owned bindings from pre-existing ones (e.g. OrbStack on 3001/3002); this causes the Manager to silently mark the forward as applied, directing traffic to the wrong process and hiding the conflict from the user.
- **Fit criterion** — when `Forwarder.Present` is called for a port already bound by a process whose pid does not match the forward's listener pid: (a) it returns a conflict sentinel, not a success; (b) the Manager does not add the entry to its applied set; (c) the conflict is surfaced in `nexus-client` log output with the conflicting pid.
- **Verification**: automated — `TestForwarderPresentConflict` in `internal/core/portfwd` exercises the distinction using a pre-bound listener and a different forward descriptor.
- **Criticality**: must
- **See also** [HPO-R-008](#hpo-r-008), [HPO-R-010](#hpo-r-010)
