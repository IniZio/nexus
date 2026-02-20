# Nexus E2E Tests

End-to-end tests for the Nexus Workspace SDK, Daemon, and OpenCode Plugin integration.

## Overview

This directory contains E2E and integration tests that verify the complete Nexus workspace workflow:

- **Integration Tests**: Test SDK + Daemon communication
- **E2E Tests**: Test full OpenCode workflow with plugin interception

## Prerequisites

- Node.js 20+
- Docker and Docker Compose
- Workspace daemon built and available

## Quick Start

### Install Dependencies

```bash
cd e2e
npm install

# Also build the workspace-sdk
cd ../packages/workspace-sdk
npm install
npm run build
```

### Run Integration Tests

```bash
npm run test:integration
```

### Run E2E Tests

```bash
npm run test:e2e
```

### Run All Tests

```bash
npm test
```

## Docker-Based Testing

### Start Environment

```bash
npm run docker:up
```

### Stop Environment

```bash
npm run docker:down
```

### Build Images

```bash
npm run docker:build
```

## Test Structure

```
e2e/
├── tests/
│   ├── integration/
│   │   └── sdk-daemon.test.ts    # SDK + Daemon integration tests
│   ├── e2e/
│   │   └── opencode-workflow.test.ts  # Full OpenCode workflow tests
│   ├── fixtures/
│   │   └── test-workspace/       # Test fixture files
│   └── setup.ts                  # Test setup and utilities
├── docker-compose.test.yml        # Docker Compose configuration
├── jest.config.js                # Jest configuration
└── package.json                  # Dependencies
```

## Test Scenarios

### Integration Tests

- File write/read operations
- File existence checks
- Directory creation and listing
- File deletion
- Command execution (echo, pwd, failing commands)
- Connection management

### E2E Tests

- File interception (workspace vs local)
- Command interception
- Development workflow simulation
- Workspace isolation

## CI/CD

GitHub Actions workflow is defined in `.github/workflows/e2e.yml`.

### Running in CI

The CI pipeline:
1. Builds the workspace-daemon Docker image
2. Sets up Node.js
3. Installs dependencies
4. Runs integration tests
5. Runs E2E tests
6. Uploads test results

## Troubleshooting

### Container fails to start

Check Docker is running:
```bash
docker ps
```

Check daemon logs:
```bash
docker logs nexus-workspace-daemon-test
```

### Tests timeout

Increase timeout in `jest.config.js`:
```javascript
testTimeout: 120000, // 2 minutes
```

### Port already in use

Stop any existing containers:
```bash
docker-compose -f docker-compose.test.yml down
```

## Notes

- Tests use Testcontainers for isolated Docker-based testing
- Each test suite starts its own daemon container
- Containers are cleaned up after each test suite
- Tests are designed to be parallelizable where possible
