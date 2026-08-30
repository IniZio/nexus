# WIP checkpoint — nexus3/hotswap-04 (supervisor detach verb)

Checkpoint commit ahead of a VM recreation (nested virtualisation being enabled
for live detach/handoff proof against a real inner nexus3 VM). This captures
state for whoever picks this up next.

## Finished

- `/supervisor/detach` and `/supervisor/handoff` IPC verbs
  (`internal/supervisor/ipc.go`, new `internal/supervisor/handoff_verb.go`).
  Detach exits the supervisor process without tearing the VM down (skips
  `svc.Stop`/`svc.Remove`); the existing SIGTERM / `/supervisor/stop` teardown
  path is untouched and pinned by a mutation-proven test
  (`TestDetachStopDistinction_DetachDoesNotCloseStopCh`).
- Fail-safe ownership guarantee (D-HSH-08): on handoff failure or refusal the
  outgoing supervisor keeps `detachCh` open and never releases the perimeter —
  only a *dup'd* fd is handed to `handoff.Offer`. Covered by
  `TestHandoffHTTP_RefusalDoesNotCloseDetachCh` /
  `TestHandoffVerb_RefusalDoesNotDetach`.
- Fixed D-HSH-09 (slice 01 audit finding): `supervisor.go:422`'s blind
  `defer os.Remove(sockPath)` could unlink a replacement supervisor's freshly
  bound socket. Replaced with `removeOwnSocket`, which snapshots via
  `os.Stat(sockPath)` before and after and only unlinks if `os.SameFile`
  confirms it's still the same inode — fstat-on-listener-fd does NOT work for
  this (AF_UNIX fstat reports the socket's own sockfs identity, not the
  filesystem directory entry). Test: `TestRemoveOwnSocket_LeavesReplacementSocketAlone`.
- `perimeter.PerimeterFD()` — dup'd `*os.File` accessor for the R1 perimeter
  socketpair fd, for handoff transport. Own independence test
  (`TestPerimeterFD_DupIsIndependent`).
- 4 mutation-proof RED→GREEN pairs recorded (detach/stop distinction, refusal
  non-detach, socket-unlink guard, fd-dup independence). All confirmed GREEN
  as of this commit via manually-guarded `go build`/`go vet`/`go test`
  (`GOMAXPROCS=2 -p 1 -parallel 1 -count=1`, scoped to
  `internal/supervisor/...` and `internal/core/perimeter/...` — no VM boot).

## Half-written / not done

- **Adopt-mode supervisor boot (deliverable #3) — NOT implemented.** Requires
  a netns-adopt seam from slice 03 (`ch_netns.go` / `AdoptNetnsRuntime`-shaped
  API) that does not exist yet: `nexus3/hotswap-03` has zero commits and zero
  diff vs `develop` in this checkout. Only the requirement is documented in
  code comments on `handoffFn`'s payload builder (fresh `govern.Governor`
  from `Payload.Governor` bounds, no state transfer, per loop.go:58-76/145-173).
  Next step once slice 03 lands: implement `RunAdopted` (or equivalent) that
  skips `svc.Start`, rebuilds the perimeter around the received fd via
  `handoff.Accept`, and boots a fresh `govern.Governor`.
- `Payload.CA`, `Payload.Credentials`, `Payload.Virtiofs` are left unpopulated
  in `handoffFn` — accessors don't exist yet on `mitm.Proxy`/`cred.Broker`.
  Flagged in code but not wired.
- Never proven end-to-end against a live VM — all coverage so far is
  hermetic/unit-level on the supervisor/perimeter packages. That's exactly
  the gap the incoming nested-virtualisation environment is meant to close.
- Ticket file's Evidence/Decision/Ruled-out sections were never filled in —
  see "premises found wrong" below for why.

## What I was about to do next

Wait for the VM recreation, then use real `/dev/kvm` nested virtualization to
boot two inner nexus3 supervisors and drive an actual detach → handoff →
adopt cycle end-to-end, replacing the hermetic-only test coverage above with
live proof (especially the fault-injection scenario: kill the incoming side
mid-handoff and confirm the outgoing supervisor still owns the VM and egress
still works, against a real perimeter/VM instead of mocked fds).

## Premises from the brief found to be wrong (this environment)

- **`.groundwork/motives/nexus3-host-supervisor-hotswap/` does not exist in
  this checkout.** No motive.md, no ticket file 04. Could not read grounded
  premises from there; worked from the task brief text directly instead.
- **`make` is not installed** in this environment (not in PATH, not in
  `/usr/bin`). CLAUDE.md's memory-safety rule (mandatory `make build`/`make
  vet`/`make test`, `choom`/`systemd-run MemoryMax` guards) could not be
  followed as written. Worked around manually with capped `GOMAXPROCS=2`,
  `-p 1 -parallel 1`, scoped test paths only, no VM boot — but this does NOT
  replicate the Makefile's cgroup guards. Flag before running anything wider.
- **`doc/specs/per/PER-R-001` as a spec file does not exist in this
  checkout.** Could not quote its literal text for the TBD-7 amendment. Used
  the doc comment at `internal/supervisor/supervisor.go:1-15` ("boots and
  owns the VM (via svc.Start → driver.Start)") as the load-bearing text that
  contradicts adopt-mode instead.
- **`handoff.Offer`/`Accept`'s rendezvous cannot ride `SOCK_DGRAM`** the way
  the package's own test helper (`unixgramPair`, built via `socketpair(2)`)
  implies for a Listen+Dial-by-path rendezvous — a bidirectional Offer/Ack
  exchange over `unixgram` needs both ends explicitly bound+connected first,
  which a path-based Dial doesn't give you. Used `SOCK_STREAM` ("unix"
  network, same as the existing IPC socket) at the `/supervisor/handoff` HTTP
  verb layer instead. `handoff.Offer/Accept` themselves are unchanged and
  transport-agnostic — this was a transport choice one layer up, not a change
  to the handoff package.
- **`nexus3/hotswap-03` has not started**: zero commits, zero diff vs
  `develop`. The brief describes it as "running CONCURRENTLY" — in this
  checkout it hasn't begun, so the adopt-mode seam this slice depends on
  (deliverable #3) is unavailable by construction, not by omission.
