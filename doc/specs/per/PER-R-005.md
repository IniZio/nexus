---
id: PER-R-005
concept: C-PER
summary: "End-to-end live proof: supervisor survives CLI exit, in-VM claude receives a real 200 from api.anthropic.com, git clone egress succeeds, zero credentials appear on the guest disk, and orca destroy tears everything down."
criticality: must
verification: manual
status: active
trace: AC-5, D-PP-01, D-PP-03, D-PP-04
scope: all orca sandboxes using dedicated cred store (DefaultDedicatedCredStorePath present)
---

**Scope: all profiles using the dedicated cred store.** From A5 (adopt-openshell-lessons, 2026-09-21), the claude-code live mount is retired; the zero-cred-on-disk assertion now applies to claude-code as well — no real credential appears on the guest filesystem for any profile. The A2-spec-conflict scope narrowing is superseded by D-15. See CRED-R-001 for the A5 outcome.

**When** `nexus orca create` completes and the launching CLI has exited, the supervisor **shall** keep egress alive such that (a) in-VM `claude` receives a real HTTP 200 from `api.anthropic.com`, (b) `git clone` over HTTPS succeeds, (c) no real credential token appears on the guest disk, and (d) `nexus orca destroy` terminates the supervisor, perimeter, and VM cleanly.

- **Why** — Individual unit tests verify components in isolation; the end-to-end proof is the only check that the wiring from spawn → perimeter → MITM → broker → guest credential placeholder closes correctly under real infrastructure.
- **Fit criterion** — `TestSupervisorS4LiveEgress`: the test spawns an orca sandbox, waits for READY, kills the spawning process, then asserts (a) HTTP 200 from `api.anthropic.com`, (b) a git clone succeeds, (c) `find / -name '*.credentials.json' -not -empty` on the guest returns nothing, and (d) destroy returns without error.
- **Verification** manual · **Criticality** must · **Source** nexus-persistent-perimeter#D-PP-01
- **Code** `internal/test/selfhost/supervisor_s4_test.go:483` (`TestSupervisorS4LiveEgress`), `internal/supervisor/supervisor.go:573` (D-PP-04 zero-cred-in-guest annotation)

### Manual procedure

1. Boot a nexus host with KVM and live Anthropic credentials in `~/.claude/.credentials.json`.
2. Run `TMPDIR=/tmp go test -tags integration,live -run TestSupervisorS4LiveEgress ./internal/test/selfhost/...`.
3. Confirm the test log shows: `200 OK` from `api.anthropic.com`, `git clone` exit 0, credential scan empty, destroy exit 0.
