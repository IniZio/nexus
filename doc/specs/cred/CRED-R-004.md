---
id: CRED-R-004
concept: C-CRED
summary: "A push to a remote outside the repo's .nexus/config.yaml egress policy is refused with an error naming the policy and the rejected host+owner/repo; a push to an in-policy repo but out-of-allowlist ref is refused naming the ref; non-git SSH commands and flag-injection attempts are refused."
criticality: must
verification: automated
status: active
trace: AC-4
---

The host git SSH relay **shall** enforce two guards before forwarding any request:

1. **Host + repo allowlist** — derived from `.nexus/config.yaml` `egress.policy`. A push to a repo whose `<host> <owner/repo>` pair is not in the policy **shall** be refused immediately with a pkt-line ERR frame containing `nexus3: refused by egress policy: <host> <owner/repo> not in .nexus/config.yaml egress.policy`.

2. **Branch allowlist** — on `git-receive-pack` (push) only, the relay **shall** parse the pkt-line ref negotiation and check each pushed ref against `AllowedBranches` (default: `refs/heads/nexus3/**`). A ref not in the allowlist **shall** be refused with `nexus3: refused: ref <refname> not in allowed branches`.

Only `git-upload-pack` and `git-receive-pack` **shall** be forwarded. Any other SSH command — including interactive shells, `sftp`, and flag-injection via `-oProxyCommand=…` — **shall** be refused with a non-zero exit code.

- **Why** — Pushes over SSH bypass the MITM; without an explicit policy guard a guest could push to any reachable remote. Branch enforcement (parity with `proxy.go:438-479`) ensures the SSH path does not regress the ref-allowlist control.
- **Fit criterion** — `git push inizio HEAD` (inizio fork, not in policy) exits non-zero with `refused by egress policy` in stderr; `git push origin HEAD:refs/heads/main` exits non-zero with `refused: ref refs/heads/main` in stderr; `ssh git@github.com whoami` exits non-zero. Unit tests are authoritative for the parsing and refusal logic; live tests cover the E2E path.
- **Verification** automated · **Criticality** must · **Source** nexus3-mount-creds-ssh-relay#AC-4
- **Tests** `TestRelayRefusalIsPktLineERR` (`internal/core/gitssh/relay_test.go:198`); `TestRelayRefusesPolicy` (`relay_test.go:245`); `TestRelayReceivePack_RefBlocked` (`relay_test.go:357`); `TestDeriveAllowlist_ExampleApp` (`internal/core/gitssh/policy_test.go:24`); `TestDeriveAllowlist_Deduplication` (`policy_test.go:66`); `TestParseCommand` (`internal/core/gitssh/command_test.go:10`)
