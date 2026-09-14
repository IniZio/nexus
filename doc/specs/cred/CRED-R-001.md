---
id: CRED-R-001
concept: C-CRED
summary: "A freshly created claude-code worktree sandbox has the host ~/.claude live-mounted read-write at /root/.claude; the guest starts with valid credentials without login or onboarding, and after a forced expiry the guest refreshes the token itself while the host session keeps working."
criticality: must
verification: live
status: active
trace: AC-1
---

In a freshly created `claude-code` sandbox, **`/root/.claude` shall be the host's `~/.claude` live and read-write** (virtiofs `domain.LiveMount`). The guest Claude process **shall** start without requiring login or onboarding (no `claude login` prompt), and **shall** refresh the access token itself when `expiresAt` in `.credentials.json` is set to the past — with the rotated token visible on both the host and the guest.

The host CredGuardian (`cred.CredGuardian`) **shall** proactively refresh the credential 30 minutes before expiry (`guardianRefreshAhead`), serialised by an advisory `flock(2)` on a sidecar lock file so concurrent guardians do not double-refresh.

No `CLAUDE_CODE_OAUTH_TOKEN` placeholder **shall** appear in the guest environment, and `/run/nexus3/cred.env` **shall** carry no Claude OAuth entry.

- **Why** — Retiring the dedicated credential store eliminates the refresh-race and rotation-revocation hazards that arise when two processes share an access token with zero overlap.
- **Fit criterion** — In guest: `touch /root/.claude/.nexus3-probe-<id>` creates a file visible on the host; `grep -c CLAUDE_CODE_OAUTH_TOKEN /proc/$(pgrep -n claude)/environ` = 0. On host: setting `expiresAt` to the past, running a prompt in the guest, and running a prompt on the host both succeed and `.credentials.json` shows a fresh `expiresAt`. Live only.
- **Verification** live · **Criticality** must · **Source** nexus3-mount-creds-ssh-relay#AC-1
- **Tests** `TestCredGuardian_NoRefreshWhenFresh` (`internal/core/perimeter/cred/guardian_test.go:58`); `TestCredGuardian_RefreshesWhenExpiring` (`guardian_test.go:72`); `TestCredGuardian_TwoRacingGuardians` (`guardian_test.go:111`)
