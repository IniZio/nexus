---
id: PER-R-003
concept: C-PER
summary: "nexus3 orca create spawns the detached supervisor via double-fork/setsid, awaits its READY signal (supervisor.pid present), emits connection JSON; nexus3 orca destroy signals supervisor teardown."
criticality: must
verification: automated
status: active
trace: AC-3, D-PP-01
---

**When** `nexus3 orca create` runs, it **shall** spawn the detached supervisor via `supervisor.SpawnDetached` (double-fork / setsid so the child outlives the parent), await the READY signal (presence of `supervisor.pid`), persist `SupervisorPID` and `SupervisorSock` onto the sandbox record, and emit connection JSON; and **when** `nexus3 orca destroy` runs, it **shall** signal teardown via `supervisor.StopSupervisor` followed by sandbox removal.

- **Why** — Without an explicit READY wait the caller cannot know the perimeter is active before handing the connection JSON to the orchestrator; without persisting the sock path, destroy cannot locate the supervisor to stop it.
- **Fit criterion** — After `orca create` returns, the sandbox record carries non-zero `SupervisorPID` and non-empty `SupervisorSock`; after `orca destroy` the supervisor process is gone and the sandbox record is removed.
- **Verification** automated · **Criticality** must · **Source** nexus3-persistent-perimeter#D-PP-01
- **Code** `internal/cli/cmd_orca.go:699` (`supervisor.SpawnDetached`), `:703` (sock path), `:709` (`svc.SetSupervisor`), `:845` (`supervisor.StopSupervisor`); `internal/core/domain/sandbox.go:88-92` (`SupervisorPID`, `SupervisorSock` fields); `internal/supervisor/spawn_linux.go:149` (`SpawnDetached`)
