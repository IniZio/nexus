---
id: CRED-R-005
concept: C-CRED
summary: "gh pr create from inside the sandbox succeeds via the MITM + brokered GH_TOKEN; cross-repo gh api returns 403; the guest holds only the 64-hex GH_TOKEN placeholder."
criticality: must
verification: live
status: active
trace: AC-5
---

The GitHub `GH_TOKEN` broker and MITM path **shall** be retained unchanged (operator decision, not revisited here). From inside a claude-code sandbox:

- `gh pr create --fill -R <in-policy-repo>` **shall** succeed (HTTP 201).
- `gh api repos/<out-of-policy-repo>` **shall** return HTTP 403.
- The guest **shall** hold only the 64-hex `GH_TOKEN` placeholder, not a real GitHub token, confirming the placeholder-swap path is still live.

The git SSH relay path **shall** not affect `gh`/HTTPS GitHub API traffic. HTTPS requests to `github.com`, `api.github.com`, and `uploads.github.com` **shall** continue to route through the MITM proxy.

- **Why** — `gh` and GitHub API calls depend on the MITM broker path; the SSH relay is additive and must not regress it. This is a regression guard.
- **Fit criterion** — In guest: `echo $GH_TOKEN` shows the 64-hex placeholder (not the real token); `gh pr create --fill` exits 0; `gh api repos/<out-of-policy>` exits non-zero with a 403. Live only.
- **Verification** live · **Criticality** must · **Source** nexus3-mount-creds-ssh-relay#AC-5
- **Tests** `TestSupervisorEgress_GHTokenPlaceholder` (perimeter supervisor egress tests, `internal/core/perimeter/supervisor_egress_test.go`)
