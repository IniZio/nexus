---
id: PER-R-004
concept: C-PER
summary: "The supervisor's long-lived credential broker is fed by cred.Refresher (from DefaultDedicatedCredStorePath), not StaticCredentialSource, giving zero-cred auth with automatic token rotation; the MITM CA is seeded into the guest on the persistent path."
criticality: must
verification: automated
status: active
trace: AC-4, D-PP-03
scope: all profiles (claude-code, cursor, opencode, oh-my-pi)
---

**Scope: all profiles.** From A5 (adopt-openshell-lessons, 2026-09-21), the claude-code live mount is retired; `ClaudeCodeProfile` now uses the broker/placeholder/MITM path on equal footing with cursor, opencode, and oh-my-pi. The A2-spec-conflict scope narrowing to non-claude-code profiles (2026-09-21) is superseded by D-15. See CRED-R-001 for the A5 outcome.

The supervisor **shall** wire a `cred.Refresher` (loaded from `service.DefaultDedicatedCredStorePath()`) into the long-lived `cred.Broker` rather than using `StaticCredentialSource`, so that the in-guest agent receives valid bearer tokens with automatic rotation; and the supervisor **shall** seed the MITM CA certificate into the guest via `service.SeedCA` / `GuestCACertPath` so the guest trusts the intercepting proxy.

- **Why** — `StaticCredentialSource` does not rotate; a long-lived sandbox whose short-lived access token expires loses egress auth. Without the CA seed the guest rejects the MITM proxy's TLS certificate, breaking HTTPS egress immediately on boot.
- **Fit criterion** — `TestSupervisorS4PlaceholderInGuest` (`internal/test/selfhost/supervisor_s4_test.go:137`): the test uses `seedAgentCreds=false` (no AgentName) per D-PD-32 security narrowing; it asserts `GuestCredEnvPath` is **absent** for a non-agent sandbox, the MITM CA is present at `GuestCACertPath`, and no real cred material appears on guest disk. **Stale-description notice:** the fit criterion previously read "CLAUDE_CODE_OAUTH_TOKEN inside the VM holds a placeholder value" — that description no longer matches the test's assertion after D-PD-32 narrowing. The test exists and runs; only its prior description was stale.
- **Verification** automated · **Criticality** must · **Source** nexus-persistent-perimeter#D-PP-03
- **Code** `internal/supervisor/supervisor.go:355` (`cred.NewBroker`), `:369-373` (`cred.NewRefresher` loop), `:566-580` (seed MITM CA + agent placeholder = "5d"), `internal/test/selfhost/supervisor_s4_test.go:194` (`TestSupervisorS4PlaceholderInGuest`)
