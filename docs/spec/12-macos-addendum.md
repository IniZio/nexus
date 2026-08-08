# 12 — macOS Addendum (Backlogged, Validated)

*Purpose: preserve the macOS / Apple `Virtualization.framework` design as a validated future-milestone deliverable. This is **backlogged**, not a Linux-path blocker.*

> **Status: BACKLOGGED — deferred, not abandoned.** macOS support re-enters as a milestone **after Linux self-hosts** (map ruling 2026-08-06). Every design below was **empirically validated on the `newman@minion` test host** (Darwin arm64, macOS 26.5.1, Swift 6.3.1) under pure ad-hoc entitlement signing (ticket 33). The `driver` seam (doc 02) exists to keep all of this re-addable **without core changes**: a second `driver` implementation plus `nexus3-vzd` is the whole macOS delta. Nothing here gates the Linux path.

## Ecosystem: Go core + Swift `nexus3-vzd`

The Go core is unchanged; the VZ boundary is pushed **out of process** into a Swift **`nexus3-vzd`** daemon (ticket 09). The core carries **no FFI, no `unsafe`, no entitlement**. This is what makes nexus3 multi-language by construction — and it is why the guest-agent language stayed a reversible choice behind the proto seam.

- vzd speaks **REST on a per-VM unix socket in Cloud Hypervisor's *shape* but not its verbs** (ticket 18) — verb-for-verb was never available (CH's own OpenAPI cannot express CH's restore; ~20 of its 31 endpoints have no VZ counterpart).
- **One vzd per VM** (ticket 18). Topology is locked independently of measurement on VZ's main-thread/CFRunLoop ownership: a multiplexing daemon forces N VMs to contend for one main thread (what crashed `lume`), so per-VM processes **dissolve** the serialization constraint and make `--count n` fan-out possible rather than taxed.

## Supervision on macOS

Supervision copies the Linux shape (ticket 18): the per-sandbox supervisor spawns vzd; vzd outlives a core restart; `flock` liveness is **verified identical on macOS** (XNU `vfs_vnops.c:1863-1898`, SIGKILL-safe).

- **No `PDEATHSIG` exists on Darwin** (no `prctl(2)` at all). The substitute is a **retained pipe** (EOF on any parent death), documented as explicitly **weaker than Linux**, backstopped by `flock` + a PID/`p_starttime` identity tuple (old nexus's two-tier-detection learning). launchd is not required.
- vzd's verb set must expose the VM's **actual run state**, not just aliveness (ticket 19 obligation → ticket 18), because distinguishing a running parent from a paused one is what substrate-first recovery needs.

## The VZ fork tier

- Save/restore + **APFS clonefile**. **N=3 fan-out from one host-tied save file measured at 0.87s wall** (ticket 33) — *iff* the same `VZGenericMachineIdentifier` is persisted (its 70-byte `dataRepresentation`) and reloaded per restore. A fresh config mints a random id and restore fails `VZErrorDomain code=12 "invalid argument"` before any save-file IO. vzd owns this **machine-identifier sidecar** and `validateSaveRestoreSupportWithError:`; the APFS disk clone and identity fixup stay outside it.
- **Uniform fork semantics** with Linux (ticket 13): same verbs, same state-preserving snapshot (doc 05, doc 06). The asymmetry is **cost, not features**.
- **Cost = provisioned size.** VZ has no free-page reporting and no memory hot-plug, so a branch costs its **full provisioned size**. The **balloon is a weak pressure signal, not a reclaim lever**: inflating 4096→512MB (a 3584MB ask) returned only ~+214MB at peak (~6%), decaying to +109MB by t+30s, VZ host RSS unchanged (ticket 33). 13 §6's boot-small guidance is confirmed, not softened. Suspending idle branches remains macOS's only real memory lever (would reopen ticket 20 §9 if pursued).
- In-guest **re-identity is mandatory** on macOS (VZ rejects pre-restore MAC mutation). Snapshots are **non-portable** (VZ save files are hardware-encrypted, host+account-bound), which is why a portable memory-state kind cannot exist anywhere.

## Data path: SCM_RIGHTS fd-passing

The preferred agent data path is **fd-passing** (ticket 36, verified ticket 33): vzd `dup`s a `VZVirtioSocketConnection.fileDescriptor` and hands it to the core via **SCM_RIGHTS**, so vzd owns the connection's *existence* and the core owns the byte stream. vzd stays off the keystroke path; framing is identical to Linux, so **no macOS latency tax** for ticket 29.

- **Load-bearing rule:** `VZVirtioSocketConnection.deinit` `close()`s the wrapped fd (`EBADF` after release), so **vzd MUST `dup` before releasing the Swift object** (verified end-to-end).
- Fallback if fd-passing ever fails: **vzd-relays** (and ticket 29 would inherit a latency tax) — not needed per ticket 33.

## Guest kernel: PCIe-virtio

VZ drives virtio over **PCIe** and **silently fails an MMIO-only kernel** (ticket 33 → ticket 14): the host `libkrunfw` MMIO kernel booted virtio-blk but exposed **no** virtio-fs, vsock or serial console under VZ. The macOS guest kernel config **must build in `virtio_pci`, `virtio_fs` and `vsock`** (a working PCIe-virtio kernel — puipui-linux — was needed to complete the measurements). A macOS image built without these is silently device-dead.

## Egress on macOS

The perimeter runs identically: **clawk runs gvproxy on the VZ `VZFileHandleNetworkDeviceAttachment`** (ticket 15), so the composed egress stack (doc 08) is platform-uniform above the `driver` network hook. **virtio-fs close-to-open coherence holds on VZ** (ticket 33 M4) — a host write-temp-then-`rename(2)` is seen by a freshly-exec'd guest process (3/3, no stale reads) — so credential freshness works with **no cache knob** (VZ exposes none; the `--cache=never` requirement has no macOS equivalent to set, and doesn't need one).

## Entitlements and bundling

- **Exactly one entitlement key:** `com.apple.security.virtualization` (ticket 18). crabbox's other two serve its in-process SSH proxy, which nexus3 designed away; only ticket 15 could ever reopen the set. Ad-hoc signing (`codesign -s - --entitlements`, no keychain) is sufficient (ticket 35).
- Bundling copies crabbox: **sign-then-embed, digest-keyed extract-on-first-use, macOS CI runner mandatory** (ticket 18).

## Snapshot integrity on macOS

macOS shares only the substrate-agnostic **commit marker** (the VZ save file is opaque; no length assertion) and leans on VZ's clean-failure contract: **`VZErrorRestore` on a file that "cannot be read, or contains otherwise invalid contents", with "the virtual machine state is unchanged" on failure** (Apple, since the 14.0 SDK). Ticket 33 confirmed VZ **rejects truncated save files at every depth** with `VZErrorRestore` (caveat: `code=12` is VZ's catch-all for truncation **and** machine-id mismatch). This answers doc 05 / ticket 40's open macOS question.

## Listener survival on macOS

A guest `VSOCK-LISTEN` server accepts a **brand-new** host connection after a cross-process VZ save/restore (the pre-save connection is closed by VZ across the cycle, as expected; the listen socket survives; ticket 33). So on macOS too the agent must **not** rebuild guest listeners on reattach (doc 03, doc 04).

---

*Sources: tickets 18 (vzd protocol/lifecycle, one-per-VM, retained pipe, entitlement, bundling), 33 (all macOS measurements on newman@minion: 0.87s N=3, machine-id sidecar required, fd-passing + dup rule, balloon weak, PCIe-virtio, close-to-open, truncation rejection, listener survival), 13 (VZ fork tier, cost=provisioned-size, non-portable), 09 (Swift daemon ecosystem), 15 (macOS egress half), 35 (ad-hoc signing settled), 40 (VZ integrity contract), 36 (fd-passing preferred). Map Backlogged section.*
