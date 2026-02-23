# Nexus E2E Tests

> **⚠️ STATUS: E2E tests are currently disabled**
> 
> The E2E tests depended on the `workspace-sdk` and `workspace-daemon` packages which have been removed.
> The workspace functionality has been consolidated into the `nexusd` package with a different architecture.
>
> To re-enable E2E tests, they need to be rewritten to use the `nexus` CLI instead of the workspace SDK.

## Overview

This directory previously contained E2E and integration tests for the Nexus Workspace SDK, Daemon, and OpenCode Plugin integration.

The tests were designed to verify:
- SDK + Daemon communication
- Full OpenCode workflow with plugin interception

## Current Status

The workspace functionality is now provided by the `nexusd` package with the following commands:

```bash
# Workspace management
nexus workspace create <name>
nexus workspace start <name>
nexus workspace stop <name>
nexus workspace delete <name>
nexus workspace list
nexus workspace status <name>
nexus workspace ssh <name>
nexus workspace exec <name> -- <command>
nexus workspace use <name>
nexus workspace use --clear
```

## Historical Test Structure

```
e2e/
├── tests/
│   ├── integration/
│   │   └── sdk-daemon.test.ts    # SDK + Daemon integration tests (DISABLED)
│   ├── e2e/
│   │   └── opencode-workflow.test.ts  # Full OpenCode workflow tests (DISABLED)
│   ├── fixtures/
│   │   └── test-workspace/       # Test fixture files
│   └── setup.ts                  # Test setup and utilities
├── docker-compose.test.yml        # Docker Compose configuration (DEPRECATED)
├── jest.config.js                # Jest configuration
└── package.json                  # Dependencies
```

## CI/CD

The GitHub Actions workflow (`.github/workflows/e2e.yml`) has been updated to:
1. Verify nexusd builds correctly
2. Run unit tests from the nexusd package
3. Skip the disabled E2E tests with an explanatory message

## Re-enabling E2E Tests

To re-enable E2E tests:

1. **Rewrite tests to use nexus CLI:**
   - Replace `WorkspaceClient` SDK calls with CLI exec calls
   - Use `nexus workspace create`, `nexus workspace exec`, etc.

2. **Update docker-compose.test.yml:**
   - Remove references to `nexus-workspace-daemon:test` image
   - Use the nexusd daemon instead

3. **Remove @nexus/workspace-sdk dependency:**
   - Update `e2e/package.json`
   - Remove Jest path mappings for workspace-sdk

4. **Update test scenarios:**
   - File operations via `nexus workspace exec`
   - Command execution via `nexus workspace exec`
   - Workspace lifecycle via `nexus workspace` commands

## Current Testing Strategy

Until E2E tests are rewritten:

1. **Unit tests** in `packages/nexusd/` cover core functionality
2. **Integration tests** in `packages/nexusd/test/integration/` test component interactions
3. **Manual testing** using the `nexus` CLI
4. **Dogfooding** - using Nexus for its own development

## See Also

- [Workspace Quickstart](../../docs/tutorials/workspace-quickstart.md)
- [Nexus CLI Reference](../../docs/reference/nexus-cli.md)
- [packages/nexusd/README.md](../../packages/nexusd/README.md)
