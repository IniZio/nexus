---
id: PER-R-006
concept: C-PER
summary: "A stale supervisor (dead pid, leftover sock files) is detected and cleaned up; a live supervisor is re-adopted rather than duplicated."
criticality: must
verification: automated
status: active
trace: AC-6, D-PP-01
---

**When** a sandbox is started and an existing `supervisor.pid` or `supervisor.sock` is found in `StateDir`, the system **shall** determine liveness via `supervisor.CheckAndReconcile`: if the recorded pid is dead, stale files are removed; if it is alive, the running supervisor is re-adopted without spawning a duplicate.

- **Why** — A crash or ungraceful exit leaves stale pid/sock files; spawning a second supervisor for the same sandbox would produce two competing perimeters for the same VM, causing undefined routing behaviour. Silent adoption prevents unnecessary restarts.
- **Fit criterion** — `TestSupervisorS4OrphanReconcile`: (a) for a dead pid, `CheckAndReconcile` returns `alive=false` and removes both the pidfile and sockfile; (b) for a live pid (`os.Getpid()`), it returns `alive=true` and leaves the files in place.
- **Verification** automated · **Criticality** must · **Source** nexus3-persistent-perimeter#D-PP-01
- **Code** `internal/supervisor/orphan.go:137` (`CheckAndReconcile`), `:49` (`CleanupStaleFiles`), `:25` (`PidAlive`); `internal/cli/cmd_orca.go:479` (adopt-or-spawn call site); `internal/test/selfhost/supervisor_s4_test.go:121` (`TestSupervisorS4OrphanReconcile`)
