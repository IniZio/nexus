---
id: PER-R-007
concept: C-PER
summary: "Only the credential proxy and network perimeter are ported; non-essential vsock proxies (ssh-agent, git-signing, docker-cred, notification-relay, PTY-host) are absent; the zero-cred-in-guest invariant holds for non-claude-code profiles; default non-orca sandbox flows are unchanged."
criticality: must
verification: manual
status: active
trace: AC-7, D-PP-02, D-PP-04
scope: zero-cred-in-guest clause applies to non-claude-code profiles only; vsock-proxy and baseline-flow clauses are profile-agnostic
---

**Scope (zero-cred-in-guest clause): non-claude-code profiles only.** For `ClaudeCodeProfile` (`CredDirLiveMount == true`), the live mount intentionally exposes real credentials to the guest via virtiofs; that profile cannot satisfy a zero-cred-in-guest invariant by construction. CRED-R-001 governs claude-code credential delivery. The no-extra-vsock-proxy and baseline-flow-unchanged clauses below remain profile-agnostic. Scope narrowed by A2-spec-conflict (2026-09-21).

The persistent-perimeter implementation **shall not** add the non-essential old-nexus (the predecessor) vsock proxies (ssh-agent, git-signing, docker-cred, notification-relay, PTY-host) to the supervisor, **shall not** place any real credential token on the guest disk of a non-claude-code sandbox, and **shall** leave all non-orca / non-`CreateAndBoot`-supervised sandbox flows with behaviour identical to the pre-perimeter baseline.

- **Why** — Each additional proxy enlarges the trust surface and complicates the security review; real tokens on the guest disk break the host-side broker model and violate D-PP-04 for non-claude-code profiles. Changing default flows without explicit requirement risks silent regressions in the existing sandbox user base.
- **Fit criterion** — Code review of `internal/supervisor/supervisor.go` shows no ssh-agent, git-signing, docker-cred, notification-relay, or PTY-host vsock proxy. `internal/supervisor/supervisor.go:573` carries the `D-PP-04 zero-cred-in-guest` annotation; `SeedGuestAgent` writes only a placeholder token. Existing sandbox unit tests pass without modification (see manual procedure step 2).
- **Verification** manual · **Criticality** must · **Source** nexus-persistent-perimeter#D-PP-02
- **Code** `internal/supervisor/supervisor.go:573` (D-PP-04 annotation: placeholder only), `:566-580` (seed path: only CA cert + placeholder creds written to guest)

### Manual procedure

1. `grep -r "ssh-agent\|git-signing\|docker-cred\|notification-relay\|pty-host" internal/supervisor/` — expect zero matches.
2. `make test GOTEST_PKGS=./internal/supervisor/` — expect all tests pass. **Dead fit criterion notice:** the previous step 2 read `go test ./internal/... ./cmd/...` — that command is forbidden in this repository (CLAUDE.md: bare `go test` without make guards can OOM the host); it must not be run.
3. Confirm `internal/supervisor/supervisor.go:573` comment reads "D-PP-04 zero-cred-in-guest: SeedGuestAgent writes ONLY the placeholder".
