# 11 — Accepted-Risk Register

*Purpose: the risks nexus3 ships with, and the retirement gates that have already been met. This register reflects CURRENT state — it is much smaller than ticket 12's body assumed.*

> **Read this against the current state, not ticket 12's 2026-08-03 body.** Ticket 12 named "material credentials" and "open egress" as risks to ship. **Both retirement gates are now MET** and both are recorded below as **RETIRED**, not as live risks. Do not re-list them as accepted.

## Retired gates (recorded, no longer live)

- **Material credentials + writable credential mount — RETIRED by ticket 41.** The interim was: the guest holds short-lived `material` tokens on a writable mount (ticket 20 §11 gate a). Ticket 41 verified the target agent uses the OAuth bearer **opaquely** (opaque 108-char string, not a JWT; trusts the file's `expiresAt`), so ticket 23's MITM placeholder-substitution model is viable and normative (doc 08). The guest now holds only high-entropy placeholders. Gate a is satisfied; `material` and the writable credential mount are retired.
- **Open-in-practice egress — RETIRED by ticket 15.** The interim was: egress default-deny existed only as an opt-in, host-wide, arm64-unsupported experiment (ticket 20 §11 gate b). Ticket 15 ships **default-deny per-sandbox hostname allowlists** enforced host-side (doc 08). Gate b is satisfied; open egress is retired.

## Live accepted risks (v1)

Three genuine risks remain accepted for v1:

### (i) Composed keystroke-to-echo pane latency — deferred to post-implementation measurement

The end-to-end pane latency (herdr pane → `nexus3 attach` → vsock → guest PTY) is **not yet measured**, because the composed measurement needs a `nexus3 attach` implementation that is downstream of this spec (ticket 29's own note; the `12 ← 29` edge was cut). nexus3 ships accepting this and **retires it by post-implementation measurement**; ticket 11 §3 **reopens the surface architecture only if the latency proves intolerable**. herdr's own baseline half remains measurable today. (tickets 29, 11)

### (ii) Egress v1 non-goals — allowed-host exfiltration, DoS/resource-governance, covert channels

The perimeter's guarantee is **allowlist + audit** (doc 08). Explicitly **not** defended in v1, and **backlogged**:

- **allowed-host exfiltration** — a placeholder-bearing request to an *allowed* host can carry data out;
- **DoS / resource-governance**;
- **covert channels**.

Under MITM the guest holds only placeholders, so allowlist scoping — not the transport — is the residual control. (ticket 15)

### (iii) No force-remove of a snapshot while children page from it

`snapshot rm` **refuses while children page from it**, and there is **no force-remove in v1** (accepted provisionally). The eager O(1) length check catches a truncated parent before any UFFD child can exist, so the only thing the refusal blocks is removing a snapshot with *healthy* live children. (tickets 40, 13)

## Threat-model note

The threat model is a **prompt-injected agent inside an already-contained VM; the operator is never the adversary** (ticket 20). The three live risks above are read against that model: (ii) is the one that most directly concerns the named adversary, and it is bounded by the allowlist rather than eliminated.

---

*Sources: tickets 12 (register is a named deliverable), 41 (bearer opacity → gate a), 15 (default-deny allowlists → gate b; v1 non-goals), 20 §11 (retirement gates), 29/11 (pane latency, cut `12←29` edge), 40/13 (no force-remove). Map: gates a/b met; register shrunk to (i)(ii)(iii).*
