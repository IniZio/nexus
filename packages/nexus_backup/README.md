# Nexus Runtime (`packages/nexus`)

Core Go runtime for Nexus workspace orchestration.

## What this package provides

- `nexus` CLI (flat command surface):
  - `list`, `create`, `start`, `stop`, `remove`, `ssh`, `tunnel`
  - `init`, `exec`, `doctor`
- `nexus-daemon` workspace daemon.
- Firecracker-first isolated runtime support.

## Build

```bash
cd packages/nexus
go build ./cmd/nexus/...
go build ./cmd/daemon/...
```

## Test

```bash
cd packages/nexus
go test ./...
```

## Runtime Notes

- Firecracker requires Linux/KVM.
- On macOS, use a Linux VM path (for example Lima) for firecracker-backed flows.

## Docs

- `docs/reference/cli.md`
- `docs/reference/workspace-config.md`
