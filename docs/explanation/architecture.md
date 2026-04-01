# Workspace Core Architecture

Nexus currently ships a focused remote workspace core composed of two packages.

## Components

- `packages/nexus` (Go)
  - WebSocket JSON-RPC server
  - Workspace manager and lifecycle hooks
  - Service manager and readiness checks
  - Spotlight forwarding and compose port auto-detection

- `packages/sdk/js` (TypeScript, published as `@nexus/sdk`)
  - Client connection and RPC transport
  - Workspace handle with scoped exec/fs/git/service operations
  - Spotlight and readiness APIs

## Core data flow

1. SDK connects to daemon over authenticated WebSocket.
2. Client creates/opens workspace and runs scoped operations.
3. Daemon resolves workspace path, executes handlers, and returns RPC results.
4. On `workspace.ready`, daemon can apply compose-based Spotlight forwards by convention.

## Configuration model

- Optional project config: `.nexus/workspace.json`
- Convention-over-configuration behavior for compose projects:
  - detect compose file
  - forward published ports

See reference docs for API and schema details.
