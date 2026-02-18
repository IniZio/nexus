# Installation

## Prerequisites

- Go 1.21+
- Docker 24+
- Git 2.5+

## Install from Source

```bash
# Clone the repository
git clone https://github.com/inizio/nexus
cd nexus

# Build the binary
go build -o nexus ./cmd/nexus/

# Move to PATH (optional)
sudo mv nexus /usr/local/bin/
```

## Verify Installation

```bash
nexus --version
nexus --help
```

## Docker Setup

Ensure Docker is running:

```bash
docker --version
docker ps
```

## Next Steps

- [Your First Workspace](first-workspace.md)
