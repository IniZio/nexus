# Workspace Testing Guide

## Run Tests

```bash
# Unit tests
go test ./internal/... -v

# Integration tests
go test ./test/integration/... -v

# E2E tests (requires Docker)
go test ./test/e2e/... -v

# All tests with coverage
go test ./... -cover
```

## Test Structure

- **Unit tests**: Fast, isolated, use mocks (`./internal/...`)
- **Integration tests**: Test component interactions (`./test/integration/...`)
- **E2E tests**: Full user workflows (`./test/e2e/...`)

## Coverage

Run coverage report:

```bash
go test ./... -coverprofile=coverage.out
go tool cover -func=coverage.out | grep "total:"
```

## E2E Tests

E2E tests require Docker and the `nexus` CLI. They can be skipped with:

```bash
SKIP_E2E=1 go test ./test/e2e/...
```

### Available E2E Tests

- `TestHanlunLMS` - Tests the full hanlun-lms workflow with Docker Compose
- `TestWorkspaceCreateAndDestroy` - Basic workspace lifecycle
- `TestWorkspaceExec` - Command execution in workspace

## Testing SSH Access

```bash
# Create a workspace
nexus workspace create test-ws

# Test SSH access
nexus workspace ssh test-ws

# Test SSH with specific command
nexus workspace exec test-ws -- ls -la

# Test agent forwarding
nexus workspace exec test-ws -- git clone git@github.com:user/repo.git
```

## Testing Port Forwarding

```bash
# Create workspace with services
nexus workspace create web-app --ports 3000,5173

# Verify ports are allocated
curl http://localhost:32801  # SSH port
curl http://localhost:32802  # Web service
```

---

**Last Updated:** February 2026
