# nexus

Isolated dev environments. SSH + Services with automatic port management.

```bash
curl -fsSL https://raw.githubusercontent.com/IniZio/nexus/main/scripts/install.sh | bash
```

## Quick Start

```bash
nexus login                        # Authenticate
nexus auth github                  # GitHub auth
nexus ssh setup                    # SSH key setup
nexus workspace create owner/repo  # Create workspace
nexus workspace connect name       # Connect editor
```

## Features

### 🚀 Dynamic Port Exposure
Services automatically exposed with unique host ports:
- No port conflicts between workspaces
- Direct access from host machine
- Environment variables for service discovery
- See [Port Management Guide](docs/PORT_MANAGEMENT.md)

### 🔐 Session Management
Built-in authentication and user scoping:
- Local session storage (`~/.nexus/session.json`)
- 30-day session expiration
- User-scoped workspace filtering
- See [CLI Authentication Guide](docs/CLI_AUTHENTICATION.md)

### 📦 Service Configuration
Simple YAML-based service definitions:
```yaml
services:
  postgres:
    command: "docker-compose up postgres"
    port: 5432
  app:
    command: "npm start"
    port: 3000
```

## For Development

```bash
make build          # Build binary
make test           # Run tests
make test-coverage  # Coverage report
```

## Documentation

- [Port Management](docs/PORT_MANAGEMENT.md) - Service port exposure and access
- [CLI Authentication](docs/CLI_AUTHENTICATION.md) - Session management
- [API Reference](docs/API_REFERENCE.md) - Complete API documentation
- [Configuration](docs/CONFIGURATION.md) - Workspace configuration reference

## Deployment

Local staging: `cd deploy/envs/staging && ./ops/start.sh`
