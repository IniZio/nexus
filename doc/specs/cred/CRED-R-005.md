---
id: CRED-R-005
concept: C-CRED
summary: "gh pr create is refused (MITM GraphQL default-deny); create PRs with gh api -X POST /repos/<owner>/<repo>/pulls → 201; cross-repo gh api returns 403; the guest holds only the 64-hex GH_TOKEN placeholder."
criticality: must
verification: live
status: active
trace: AC-5
---

The GitHub `GH_TOKEN` broker and MITM path **shall** be retained unchanged (operator decision, not revisited here). From inside a claude-code sandbox:

- `gh pr create` **shall** return HTTP 403. `gh pr create` uses GitHub's GraphQL API, which the MITM denies by default.
- `gh api -X POST /repos/<owner>/<repo>/pulls -f title="..." -f head="<branch>" -f base="main"` **shall** succeed (HTTP 201). This REST path routes through the MITM + brokered `GH_TOKEN`.
- `gh api repos/<out-of-policy-repo>` **shall** return HTTP 403.
- The guest **shall** hold only the 64-hex `GH_TOKEN` placeholder, not a real GitHub token, confirming the placeholder-swap path is still live.

The git SSH relay path **shall** not affect `gh`/HTTPS GitHub API traffic. HTTPS requests to `github.com`, `api.github.com`, and `uploads.github.com` **shall** continue to route through the MITM proxy.

- **Why** — `gh` and GitHub API calls depend on the MITM broker path; the SSH relay is additive and must not regress it. This is a regression guard.
- **Fit criterion** — In guest: `echo $GH_TOKEN` shows the 64-hex placeholder (not the real token); `gh pr create` exits non-zero with 403; `gh api -X POST /repos/<owner>/<repo>/pulls` exits 0 (201); `gh api repos/<out-of-policy>` exits non-zero with a 403. Live only.
- **Verification** live · **Criticality** must · **Source** nexus-mount-creds-ssh-relay#AC-5
- **Tests** live-only; see `internal/test/selfhost/mount_creds_ssh_relay_dod_test.go` (gate rerun @7c803c6: REST 201, cross-repo 403, 64-hex placeholder in guest). Placeholder-never-real-token invariant (host-agnostic, covers the GH_TOKEN broker path): `TestBrokerPlaceholder_ReturnsPlaceholderNeverRealToken` (`internal/core/perimeter/cred/placeholder_accessor_test.go:14`); `TestPlaceholderRecordHasNoRealToken` (`internal/core/perimeter/cred/cred_test.go:60`)
