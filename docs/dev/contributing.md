# Contributing

## Getting Started

1. Fork the repository
2. Clone your fork:
   ```bash
   git clone https://github.com/YOUR-USERNAME/nexus
   ```
3. Create a workspace for development:
   ```bash
   cd nexus
   nexus init
   nexus workspace create dev --template go-postgres
   nexus workspace up dev
   ```

## Development Workflow

### Using Nexus for Nexus Development

The recommended workflow is to use Nexus to develop Nexus:

```bash
# SSH into dev workspace
ssh -p <port> -i ~/.ssh/id_ed25519_nexus dev@localhost

# Make changes inside the workspace
cd /workspace
# Edit code...
```

### Code Standards

- **Go:** Follow Effective Go and standard Go conventions
- **Tests:** All new code must have tests
- **Documentation:** Update docs for new features

### Running Tests

```bash
go test ./...
```

### Building

```bash
go build -o nexus ./cmd/nexus/
```

## Architecture

See [Architecture](../explanation/architecture.md) for component details.

## Design Decisions

See [Architecture Decisions](decisions/) for ADRs.

## Submitting Changes

1. Create a feature branch
2. Make changes with tests
3. Run tests: `go test ./...`
4. Submit pull request

## Code Quality Rules

1. Single responsibility: Each function does one thing
2. No premature abstraction
3. Fail fast: Return errors immediately
4. Self-documenting: Clear names > comments
5. Test coverage for all public functions
