---
id: CRED-R-001
concept: C-CRED
summary: "A freshly created claude-code worktree sandbox has the host ~/.claude live-mounted read-write at /root/.claude; the guest starts with valid credentials without login or onboarding, and after a forced expiry the guest refreshes the token itself while the host session keeps working."
criticality: must
verification: live
status: active
trace: AC-1
scope: claude-code profile only (AgentProfile.CredDirLiveMount == true)
---

**Scope: claude-code profile only.** This requirement governs Claude credential delivery exclusively for sandboxes whose `AgentProfile.CredDirLiveMount` is `true` — currently `ClaudeCodeProfile` (`internal/core/perimeter/cred/profile.go:116`). For all other profiles (cursor, opencode, oh-my-pi), the broker/placeholder/MITM path in PER-R-004 governs; CRED-R-001 does not apply to those profiles.

In a freshly created `claude-code` sandbox, **`/root/.claude` shall be the host's `~/.claude` live and read-write** (virtiofs `domain.LiveMount`). The guest Claude process **shall** start without requiring login or onboarding (no `claude login` prompt), and **shall** refresh the access token itself when `expiresAt` in `.credentials.json` is set to the past — with the rotated token visible on both the host and the guest.

The host CredGuardian (`cred.CredGuardian`) **shall** proactively refresh the credential 30 minutes before expiry (`guardianRefreshAhead`), serialised by an advisory `flock(2)` on a sidecar lock file so concurrent guardians do not double-refresh.

No `CLAUDE_CODE_OAUTH_TOKEN` placeholder **shall** appear in the guest environment, and `/run/nexus/cred.env` **shall** carry no Claude OAuth entry.

- **Why** — Retiring the dedicated credential store eliminates the refresh-race and rotation-revocation hazards that arise when two processes share an access token with zero overlap. The hostile-rotation premise (two processes sharing one refresh token race; one gets locked out) is OPERATOR-ATTESTED, not measured in this motive — see D-9 (adopted 2026-09-21). No call log against platform.claude.com/v1/oauth/token exists in this repository.
- **Fit criterion** — In guest: `touch /root/.claude/.nexus-probe-<id>` creates a file visible on the host; `grep -c CLAUDE_CODE_OAUTH_TOKEN /proc/$(pgrep -n claude)/environ` = 0. On host: setting `expiresAt` to the past, running a prompt in the guest, and running a prompt on the host both succeed and `.credentials.json` shows a fresh `expiresAt`. Live only.
- **Verification** live · **Criticality** must · **Source** nexus-mount-creds-ssh-relay#AC-1
- **Tests** `TestCredGuardian_NoRefreshWhenFresh` (`internal/core/perimeter/cred/guardian_test.go:55`); `TestCredGuardian_RefreshesWhenExpiring` (`guardian_test.go:69`); `TestCredGuardian_TwoRacingGuardians` (`guardian_test.go:103`) — **note**: previously cited as `:107`, corrected to `:103`

**Conflict resolution (A2-spec-conflict, 2026-09-21):** PER-R-004, PER-R-005, and PER-R-007 have been scope-narrowed to non-claude-code profiles to eliminate the contradiction with this requirement. See those requirements for details. The ratify-or-retire question for CRED-R-001 itself is deferred to slice A5; D-9 tilts A5 toward ratify.
