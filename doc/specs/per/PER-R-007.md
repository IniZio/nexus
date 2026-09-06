---
id: PER-R-007
concept: C-PER
summary: "Only the credential proxy and network perimeter are ported; non-essential vsock proxies (ssh-agent, git-signing, docker-cred, notification-relay, PTY-host) are absent; the zero-cred-in-guest invariant is unbroken; default non-orca sandbox flows are unchanged."
criticality: must
verification: manual
status: active
trace: AC-7, D-PP-02, D-PP-04
---

The persistent-perimeter implementation **shall not** add the non-essential old-nexus vsock proxies (ssh-agent, git-signing, docker-cred, notification-relay, PTY-host) to the supervisor, **shall not** place any real credential token on the guest disk, and **shall** leave all non-orca / non-`CreateAndBoot`-supervised sandbox flows with behaviour identical to the pre-perimeter baseline.

- **Why** — Each additional proxy enlarges the trust surface and complicates the security review; real tokens on the guest disk break the host-side broker model and violate D-PP-04. Changing default flows without explicit requirement risks silent regressions in the existing sandbox user base.
- **Fit criterion** — Code review of `internal/supervisor/supervisor.go` shows no ssh-agent, git-signing, docker-cred, notification-relay, or PTY-host vsock proxy. `internal/supervisor/supervisor.go:573` carries the `D-PP-04 zero-cred-in-guest` annotation; `SeedGuestAgent` writes only a placeholder token. Existing sandbox unit tests (`go test ./internal/...` excluding `integration` tag) pass without modification.
- **Verification** manual · **Criticality** must · **Source** nexus3-persistent-perimeter#D-PP-02
- **Code** `internal/supervisor/supervisor.go:573` (D-PP-04 annotation: placeholder only), `:566-580` (seed path: only CA cert + placeholder creds written to guest)

### Manual procedure

1. `grep -r "ssh-agent\|git-signing\|docker-cred\|notification-relay\|pty-host" internal/supervisor/` — expect zero matches.
2. `go test ./internal/... ./cmd/...` (no integration tag) — expect all tests pass.
3. Confirm `internal/supervisor/supervisor.go:573` comment reads "D-PP-04 zero-cred-in-guest: SeedGuestAgent writes ONLY the placeholder".
