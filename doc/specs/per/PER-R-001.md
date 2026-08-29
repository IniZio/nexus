---
id: PER-R-001
concept: C-PER
summary: "Hidden nexus3 __supervisor subcommand runs as a detached per-sandbox process, boots and owns the VM, starts the network perimeter, starts a long-lived credential Broker, writes supervisor.pid and supervisor.sock, and blocks until signaled."
criticality: must
verification: automated
status: active
trace: AC-1, D-PP-01
---

The `nexus3 __supervisor` subcommand **shall** run as a detached per-sandbox process that (a) boots and owns the VM via `svc.Start`, (b) starts the network perimeter (gvproxy + MITM + netfilter) in-process, (c) instantiates a long-lived `cred.Broker`, (d) writes `supervisor.pid` and `supervisor.sock` to `StateDir` as the READY signal, and (e) blocks until signalled.

- **Why** — `orca create` and other `CreateAndBoot`-based paths never call `svc.Start`; without a dedicated long-lived owner the perimeter goroutines die the moment the launching CLI exits, leaving the in-VM agent with no egress.
- **Fit criterion** — After `nexus3 __supervisor` starts, both `supervisor.pid` (containing the process PID) and `supervisor.sock` (a connectable Unix-domain socket) are present in `StateDir`; the process remains alive and the perimeter accepts connections after the spawning process has exited.
- **Verification** automated · **Criticality** must · **Source** nexus3-persistent-perimeter#D-PP-01
- **Code** `internal/supervisor/supervisor.go:319` (`RunDetached`), `:69` (`HiddenSubcommand = "__supervisor"`), `:748-751` (pidfile write = READY signal), `cmd/nexus3/supervisor_linux.go:87` (`runSupervisorMain`)
