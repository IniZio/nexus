# Installation

## Prerequisites

- Go 1.24+
- Node.js 18+
- pnpm
- task (Taskfile runner)

## Install from Source

```bash
# Clone the repository
git clone https://github.com/inizio/nexus
cd nexus

# Install dependencies
pnpm install

# Verify Go module state
go mod download

# Build core packages
task build
```

## Verify Installation

```bash
# Run core verification
cd packages/nexus && go test ./...
cd ../sdk/js && pnpm exec tsc --noEmit && pnpm exec jest --runInBand

# Build nexus CLI
cd ../nexus && go build ./cmd/nexus/...
```

## Dogfood in this repo

Nexus is configured to dogfood itself in this repository via `.nexus/workspace.json`.

```bash
repo_root="$(pwd)"

# Initialize local workspace metadata/scripts (safe to rerun)
go run ./packages/nexus/cmd/nexus init --project-root "$repo_root" --runtime local --force

# Run project-level checks
bash .nexus/e2e/run.sh
```

## Next Steps

- [CLI and Daemon Reference](../reference/cli.md)
- [SDK Reference](../reference/sdk.md)
- [Workspace Config Reference](../reference/workspace-config.md)
