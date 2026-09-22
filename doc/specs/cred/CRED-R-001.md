---
id: CRED-R-001
concept: C-CRED
summary: "A freshly created claude-code sandbox starts with a valid CLAUDE_CODE_OAUTH_TOKEN placeholder in its guest environment (broker path, no live ~/.claude mount); the MITM proxy substitutes the real bearer token; the guest starts without login or onboarding, and after forced token expiry the broker Refresher supplies a fresh token without guest intervention."
criticality: must
verification: live
status: active
trace: AC-1
scope: claude-code profile only
---

**Scope: claude-code profile only.** This requirement governs Claude credential delivery for `ClaudeCodeProfile` (`internal/core/perimeter/cred/profile.go`). From A5 (slice adopt-openshell-lessons, 2026-09-21) the live `~/.claude` virtiofs mount is retired; the broker/placeholder/MITM path now governs claude-code on equal footing with cursor, opencode, and oh-my-pi. PER-R-004, PER-R-005, and PER-R-007 are re-widened to cover claude-code as well. **Supersedes the A2-spec-conflict resolution in a858930** (which narrowed those requirements to non-claude-code profiles to accommodate the now-retired live-mount design).

In a freshly created `claude-code` sandbox, **`CLAUDE_CODE_OAUTH_TOKEN`** shall appear in the guest environment holding a broker placeholder value (not the real token). The MITM proxy shall substitute the real bearer on every request to `api.anthropic.com` and `platform.claude.com`. The guest Claude process **shall** start without requiring login or onboarding, and **shall** survive a forced token expiry (the broker's `cred.Refresher` supplies a fresh token for the next request without guest-side refresh).

No real credential token **shall** appear on the guest filesystem, and the `~/.claude` host directory **shall not** be mounted into the sandbox.

- **Why** — D-13 (accepted 2026-09-21, adopt-openshell-lessons): A4 proved that the host CredGuardian's `flock(2)` on `<credsPath>.nexus.lock` is never taken by the in-guest Claude process, which rewrites the same file through the rw virtiofs mount via its own OAuth loop. Two writers, one lock — the flock provides no serialisation. Under the operator-attested rotation premise (D-9, 2026-09-21): platform.claude.com/v1/oauth/token rotates on every call with no overlap, so concurrent refresh gives one side 401 invalid_grant with no recovery. Retiring the mount eliminates the race entirely. The rotation premise is OPERATOR-ATTESTED and not measured in this repository; no call log against the token endpoint exists here.
- **Fit criterion** — In guest: `grep CLAUDE_CODE_OAUTH_TOKEN /proc/$(pgrep -n claude)/environ` outputs a placeholder (non-empty, not the real token); no `~/.claude` mount point on the guest filesystem. A forced expiry (set `expiresAt` to the past in the host cred store) followed by a guest prompt succeeds (MITM delivers a fresh token from the Refresher). Live only.
- **Verification** live · **Criticality** must · **Source** nexus-mount-creds-ssh-relay#AC-1
- **Tests** `TestSeedGuestAgent_OAuthPayloadHasPlaceholder` (`internal/core/service/seed_agent_test.go`); `TestSeedLoop_ForcePushWritesRealToken` (`internal/supervisor/supervisor_combined_test.go`); `TestSupervisorS4PlaceholderInGuest` (`internal/test/selfhost/supervisor_s4_test.go`)

**A5 outcome (2026-09-21, D-15):** Live-mount path RETIRED. This requirement is rewritten to mandate the broker path. The CredGuardian (`cred.CredGuardian`), the `CredDirLiveMount` capability field, and all live-mount wiring in `cmd_sandbox.go` and the supervisor are removed. See D-15 in the adopt-openshell-lessons journal.
