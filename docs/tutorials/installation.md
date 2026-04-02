# Installation

## Prerequisites

- Go 1.21+
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

# Build core packages
task build
```

## Verify Installation

```bash
# Run core verification
cd packages/nexus && go test ./...
cd ../sdk/js && pnpm exec tsc --noEmit && pnpm exec jest --runInBand
```

## Next Steps

- [Workspace Daemon Reference](../reference/workspace-daemon.md)
- [Workspace SDK Reference](../reference/workspace-sdk.md)
- [Workspace Config Reference](../reference/workspace-config.md)
