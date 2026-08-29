---
id: PER-R-002
concept: C-PER
summary: "The detached supervisor keeps egress alive after the spawning CLI exits: the guest still reaches the gateway and DNS after the launcher process terminates."
criticality: must
verification: automated
status: active
trace: AC-2, D-PP-01
---

**When** the spawning CLI process exits, the detached supervisor **shall** keep the network perimeter (gvproxy gateway + DNS + MITM proxy) alive so that in-VM traffic continues to be forwarded.

- **Why** — The core value of the detached supervisor is decoupling perimeter lifetime from the one-shot CLI. If egress dies on CLI exit, orca-launched sandboxes have no network after `nexus3 orca create` returns.
- **Fit criterion** — `TestSupervisorPostExitEgress`: after the spawning test process signals the supervisor and exits, an in-VM `curl` to a gateway endpoint returns a non-error response, proving egress is still live.
- **Verification** automated · **Criticality** must · **Source** nexus3-persistent-perimeter#D-PP-01
- **Code** `internal/supervisor/supervisor.go:451` (`svc.Start` = perimeter enters in-process goroutines), `internal/test/selfhost/supervisor_smoke_test.go:74` (`TestSupervisorPostExitEgress`)
