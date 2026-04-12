# Credential Vending Prototype

Minimal prototype validating the secure credential handling design for Nexus.

## Quick Start

```bash
# Run tests
cd packages/nexus
go test -v ./pkg/secrets/discovery/...
go test -v ./pkg/secrets/vending/...

# Run discovery on your host
go run /tmp/test_secrets.go
```

## Structure

```
experiments/2026-04-12-credential-vending-prototype/
├── JOURNAL.md              # Experiment journal
├── README.md               # This file
└── pkg/secrets/
    ├── discovery/          # Auto-detect host credentials
    │   ├── discovery.go
    │   └── discovery_test.go
    └── vending/            # Vend short-lived tokens
        ├── vending.go
        └── vending_test.go
```

## How It Works

### 1. Discovery

Scans host home directory for agent configurations:

```go
// ~/.config/codex/auth.json
// ~/.config/opencode/auth.json
// ~/.config/claude/settings.json
// ~/.config/openai/auth.json

configs, err := discovery.Discover(homeDir)
// Returns: [{Name: "codex", Type: "oauth", RefreshToken: "ghr_..."}]
```

### 2. Vending

Creates brokers for each discovered provider:

```go
svc := vending.NewService(configs)
token, err := svc.GetToken(ctx, "codex")
// Returns: {Value: "ghu_...", ExpiresAt: time.Now().Add(10m)}
```

### 3. Token Lifecycle

- **API Key:** Long-lived (24h), no refresh needed
- **Session:** Medium-lived (1h), no refresh needed
- **OAuth:** Short-lived (5-15m), host handles refresh

## Adding a New Provider

To add "Pi CLI" support:

```go
// In discovery.go
func detectPi(home string) (*ProviderConfig, error) {
    path := filepath.Join(home, ".config", "pi", "config.yaml")
    // Parse YAML, extract api_key
    return &ProviderConfig{
        Name:        "pi",
        Type:        ProviderTypeAPIKey,
        AccessToken: apiKey,
    }, nil
}

// Register in Discover()
detectors := []func(string) (*ProviderConfig, error){
    detectCodex,
    detectOpenCode,
    detectClaude,
    detectOpenAI,
    detectGitHubCLI,
    detectPi,  // <-- Add here
}
```

No changes needed to vending layer.

## Integration Points

### With Nexus Daemon

```go
// When workspace starts
configs := discovery.Discover(homeDir)
svc := vending.NewService(configs)

// Start vsock server to serve tokens to guest
go startVendingServer(svc, vsockPort)
```

### With Guest Agent

```go
// Guest requests token over vsock
token, err := vendingClient.GetToken(ctx, "codex")
// Sets env: CODEX_API_TOKEN=ghu_...
```

## Test Coverage

| Package | Tests | Coverage |
|---------|-------|----------|
| discovery | 6 | Codex, OpenCode, Claude, OpenAI, empty, multiple |
| vending | 5 | Service, API key, expiration, static broker |

## Future Work

1. **Vsock server:** Actually serve tokens to guest
2. **OAuth refresh:** Implement `RefreshableBroker`
3. **End-to-end:** Test with `codex exec` / `opencode run`
4. **Registry pattern:** Auto-discover providers without hardcoding
5. **Hot-reload:** Watch host config files for changes

## Related

- Full Design Spec: `docs/superpowers/specs/2026-04-12-secure-credential-handling-design.md`
- This prototype validates the design approach before full implementation
