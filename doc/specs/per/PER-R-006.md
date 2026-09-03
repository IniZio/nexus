---
id: PER-R-006
concept: C-PER
summary: "A stale supervisor (dead pid, leftover sock files) is detected and cleaned up; a live supervisor is re-adopted rather than duplicated; a dead supervisor whose VM is still live triggers reacquire rather than a VM stop."
criticality: must
verification: automated
status: active
trace: AC-6, D-PP-01, D-HSH-05
---

**When** a sandbox is started and an existing `supervisor.pid` or `supervisor.sock` is found in `StateDir`, the system **shall** determine liveness via `supervisor.CheckAndReconcile`: if the recorded pid is dead, stale files are removed; if it is alive, the running supervisor is re-adopted without spawning a duplicate.

**When** `nexus3 recover` encounters a sandbox whose supervisor pid is dead but whose `NetnsChildPID` is still alive (the VM's netns child process survived the supervisor crash), the system **shall** spawn a reacquire supervisor (`nexus3 __supervisor --reacquire`) that attaches to the existing VM rather than stopping it. The VM **shall not** be killed or rebooted solely because its supervisor process died.

- **Why** — A crash or ungraceful exit leaves stale pid/sock files; spawning a second supervisor for the same sandbox would produce two competing perimeters for the same VM, causing undefined routing behaviour. Silent adoption prevents unnecessary restarts. Extending the same principle to the live-VM/dead-supervisor class means a supervisor crash is recoverable without a guest reboot — consistent with the adopt-mode invariant that the VM's identity is never destroyed when the perimeter alone is what needs rebuilding.
- **Fit criterion (stale supervisor)** — `TestSupervisorS4OrphanReconcile`: (a) for a dead pid, `CheckAndReconcile` returns `alive=false` and removes both the pidfile and sockfile; (b) for a live pid (`os.Getpid()`), it returns `alive=true` and leaves the files in place.
- **Fit criterion (live-VM/dead-supervisor)** — When `nexus3 recover` runs against a sandbox whose supervisor is dead but whose `NetnsChildPID` is still alive, the sandbox is classified as adoptable and a reacquire supervisor is spawned; the sandbox's state transitions to `running` with a new `supervisor.pid`/`supervisor.sock` in `StateDir`, and the original `NetnsChildPID` process remains running throughout.
- **Verification** automated · **Criticality** must · **Source** nexus3-persistent-perimeter#D-PP-01, nexus3-host-supervisor-hotswap#D-HSH-05
- **Code** `internal/supervisor/orphan.go:137` (`CheckAndReconcile`), `:49` (`CleanupStaleFiles`), `:25` (`PidAlive`); `internal/cli/cmd_orca.go:488` (adopt-or-spawn call site); `internal/supervisor/reacquire.go:289` (`RunReacquire` — live-VM/dead-supervisor reacquire); `internal/test/selfhost/supervisor_s4_test.go:121` (`TestSupervisorS4OrphanReconcile`)
