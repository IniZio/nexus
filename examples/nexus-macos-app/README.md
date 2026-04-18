# nexus-macos-app

A dog-food example: build the Nexus macOS Swift package inside a Nexus workspace.

## What it demonstrates

- Using Nexus to develop Nexus itself (dog-fooding)
- A workspace where `probe` is a real build gate — the workspace only reports ready when `swift build --configuration release` succeeds
- Minimal lifecycle with no port forwarding needed

## Quick start

```bash
# Point nexus create at the repo root (not this subdirectory)
nexus create --project /path/to/nexus

# The workspace probe runs:
#   swift build --configuration release -C packages/nexus-swift
# The workspace reports ready only when the build succeeds.

# Or use make directly inside the workspace:
nexus exec <workspace-id> -- make build
```

## Workspace config

`.nexus/workspace.json`:

| Field | Value |
|---|---|
| `lifecycle.probe` | `swift build --configuration release -C packages/nexus-swift` |
| `lifecycle.start` | `echo nexus-swift build workspace ready` |
| `ports` | none |

## Notes

- The probe doubles as the readiness check: if the Swift package fails to compile, the workspace is not considered ready.
- This pattern is useful for CI-style workspaces where "ready" means "builds green".
