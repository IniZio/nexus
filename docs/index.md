# Nexus

Nexus is the remote workspace core for this repository:

- `packages/nexus` (Go): daemon runtime + `nexus` CLI (`init`, `doctor`, `exec`)
- `packages/sdk/js` (TypeScript): `@nexus/sdk` client library

## Core capabilities

- Firecracker-first isolated runtime workflow
- Project-level workspace config via `.nexus/workspace.json`
- Probe/test orchestration through `nexus doctor`
- SDK + daemon RPC contract for workspace automation

## Start here

- `docs/reference/cli.md`
- `docs/reference/sdk.md`
- `docs/reference/workspace-config.md`
- `docs/tutorials/installation.md`
