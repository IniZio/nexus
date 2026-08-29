---
id: PER-R-004
concept: C-PER
summary: "The supervisor's long-lived credential broker is fed by cred.Refresher (from DefaultDedicatedCredStorePath), not StaticCredentialSource, giving zero-cred auth with automatic token rotation; the MITM CA is seeded into the guest on the persistent path."
criticality: must
verification: automated
status: active
trace: AC-4, D-PP-03
---

The supervisor **shall** wire a `cred.Refresher` (loaded from `service.DefaultDedicatedCredStorePath()`) into the long-lived `cred.Broker` rather than using `StaticCredentialSource`, so that the in-guest agent receives valid bearer tokens with automatic rotation; and the supervisor **shall** seed the MITM CA certificate into the guest via `service.SeedCA` / `GuestCACertPath` so the guest trusts the intercepting proxy.

- **Why** — `StaticCredentialSource` does not rotate; a long-lived sandbox whose short-lived access token expires loses egress auth. Without the CA seed the guest rejects the MITM proxy's TLS certificate, breaking HTTPS egress immediately on boot.
- **Fit criterion** — `TestSupervisorS4PlaceholderInGuest`: after the supervisor seeds the guest, `CLAUDE_CODE_OAUTH_TOKEN` inside the VM holds a placeholder value (not a raw credential), the broker maps that placeholder to the refreshed real token, and the MITM CA is present at `GuestCACertPath`.
- **Verification** automated · **Criticality** must · **Source** nexus3-persistent-perimeter#D-PP-03
- **Code** `internal/supervisor/supervisor.go:355` (`cred.NewBroker`), `:369-373` (`cred.NewRefresher` loop), `:566-580` (seed MITM CA + agent placeholder = "5d"), `internal/test/selfhost/supervisor_s4_test.go:194` (`TestSupervisorS4PlaceholderInGuest`)
