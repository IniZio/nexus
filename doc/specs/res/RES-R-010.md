---
id: RES-R-010
concept: C-RES
summary: "nexus3 reap is liveness-gated: never deletes a resource owned by a sandbox with a live process, open handle, or fresh lease; ambiguity resolves to keep."
criticality: must
verification: manual
status: active
trace: D-HSH-24, AC-7a, AC-7b
---

`nexus3 reap` **shall** be liveness-gated: it must never delete a resource — disk slot, state directory, volume, named lock — that is owned by a sandbox whose supervisor process is alive, whose netns child is alive, or that holds an unexpired resource lease. When liveness cannot be determined, the ambiguity resolves to keep.

The builder cache-disk slot lease is the concrete resource this requirement addresses in the hot-swap motive (D-HSH-24). The slot flock must be held by the **supervisor** process, not the launching CLI, so the flock expires with the VM rather than with the CLI. A reap that runs while the supervisor is alive must see the slot as busy and leave it alone.

- **Fit criterion (AC-7a — lease ownership structure)** — After the CLI that selected the cache-disk slot has exited, the slot still reads busy (the supervisor holds the flock). A second slot selection concurrent with a live supervisor for slot N is routed to a different slot. The lease dies when the supervisor exits. Verified by unit test in `internal/supervisor/cachedisk_lease_test.go` and `internal/cli/builder_supervisor_driver_cachedisk_test.go`; covers the ownership structure, not a live CH write-lock collision.

- **Fit criterion (AC-7b — live CH write-lock collision, UNMET)** — Kill the CLI while a builder VM holds cloud-hypervisor's write lock on a disk image; start a second build immediately; assert the second build takes a different slot or waits rather than crashing with "Error locking disk images". This criterion requires a real host with capacity to run two concurrent builder VMs. **Blocked on host capacity** (root at 98%, three live sandboxes); do not read AC-7a's unit coverage as satisfying this criterion — no cloud-hypervisor write-lock collision was observed in any test in this motive.
