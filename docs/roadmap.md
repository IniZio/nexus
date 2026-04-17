# Roadmap

Feature priorities and upcoming work for Nexus.

## Current Focus

### High Priority

- **Daemon file size refactor** — `workspacemgr/manager.go` (~1268 lines) and `handlers/workspace_manager.go` (~1246 lines) need decomposition into focused sub-packages. Both severely exceed the ≤400 line orchestration limit.
- **Lima removal cleanup** — Remove Lima/process-sandbox driver references from Swift package (`ConfigSyncManager.swift`), CI workflows (`.github/workflows/ci.yml`), and integration harness comments.
- **macOS seatbelt backend removal** — The `lima` and `seatbelt` backend strings appear in `ConfigSyncManager.swift`. These should be removed since Firecracker is the only supported VM backend.

### Medium Priority

- **E2E test documentation** — `docs/guides/testing.md` needs completion covering harness setup, environment variables, and how to add new test cases.
- **Agent skills documentation** — `docs/guides/agent-skills.md` for contributors wanting to use or extend Nexus skills.
- **roadmap.md** — This document; needs regular updates as priorities shift.

### Low Priority / Exploratory

- **`.worktrees` cleanup** — After fork operations, orphaned worktree directories may remain. Document cleanup strategy.
- **Spotlight tunnel persistence** — Tunnel ports are stored in workspace metadata but not restored on daemon restart.

## Completed

- Firecracker-only runtime (Lima removed)
- Process sandbox driver as VM fallback
- JSON-RPC over WebSocket daemon API
- TypeScript SDK (`@nexus/sdk`)
- macOS app (NexusApp) embedding the daemon
- Conventional commit enforcement
- Signed release manifests

## Known Constraints

- Firecracker requires Linux with KVM. macOS cannot run Firecracker directly; the macOS app delegates to a remote Linux daemon.
- Integration tests (`//go:build integration`) require a running daemon with accessible port.
- E2E flows require `NEXUS_E2E_STRICT_RUNTIME=0` locally when no VM runtime is installed.

## Contributing

See `CONTRIBUTING.md` for setup, build, test, and PR guidelines.