---
id: RES-R-013
concept: C-RES
summary: "The memory governor is re-constructed and re-started by an adopted supervisor; resize polling resumes against the reacquired VM without rebooting the guest."
criticality: must
verification: manual
status: active
trace: AC-5
---

An adopted supervisor **shall** construct a fresh memory governor and start its adaptive poll loop against the already-running VM. After the supervisor replacement completes, the governor must emit `govern.loop.started` (`internal/core/govern/loop.go:170`) in the new supervisor's structured log, and any subsequent memory-pressure event must produce a `govern.memory.grow` (`internal/core/govern/memory.go:607`) or `govern.memory.shrink` (`internal/core/govern/memory.go:605`) entry with no `govern.memory.resize_error` (`internal/core/govern/memory.go:596`) entry. A violation is any supervisor replacement that leaves the VM running but produces no governor poll output, or that produces `govern.memory.resize_error` instead of a successful resize event.

The governor is fully stateless between instantiations: all control-law fields (`growCount`, `shrinkCount`, `lastResizeTime`, `latest`, `prevSwapUsed`, `agentOutdated`) are transient, initialised to zero by `govern.New`, and rebuilt from the first live sample (`internal/core/govern/loop.go:65–82`). The adopted path constructs the governor at `internal/supervisor/serve_adopted.go:165–169` using a `govern.Config` with a fresh `Resizer`, `VsockTelemetry`, and `Bounds` drawn from the persisted sandbox config, and starts it with `go gov.Run(ctx)` at line 175. This mirrors the boot-and-own path at `internal/supervisor/supervisor.go:600–604,612`. The `Run` function (`internal/core/govern/loop.go:151`) begins with a 10 s boot-delay then enters the adaptive poll loop; because governor state is rebuilt from scratch on each `New`, the adopted supervisor's loop is independent of any state the outgoing supervisor held.

- **Fit criterion (AC-5)** — After `supervisor-upgrade` completes, drive a memory-pressure event inside the guest (e.g. `stress-ng --vm 1 --vm-bytes 80%`). Assert: (1) the **new** supervisor's log contains at least one `govern.loop.started` line, and (2) at least one `govern.memory.grow` entry appears, and (3) no `govern.memory.resize_error` entry appears. No cloud-hypervisor restart must occur (`/proc/sys/kernel/random/boot_id` unchanged). A replacement that produces no governor output at all, or whose first resize attempt logs `govern.memory.resize_error` rather than `govern.memory.grow`, fails this criterion.
