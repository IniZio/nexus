# Workspace SDK

There is currently no supported public npm package for a Nexus Workspace SDK.

## Current Status

- A public npm workspace SDK package is not shipped as a supported surface in this repository.
- User workflows are supported through the CLI (`nexus workspace ...`) and daemon (`packages/nexusd`).

## If You Need Automation Today

Use one of these implemented paths:

1. `nexus workspace exec <name> -- <command>` for scripted command execution.
2. `nexus workspace ssh <name>` for interactive sessions.
3. Daemon HTTP/WebSocket endpoints in `packages/nexusd/pkg/server/server.go` for internal integrations.

## References

- `docs/reference/nexus-cli.md`
- `docs/reference/workspace-daemon.md`
