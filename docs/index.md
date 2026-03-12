# Nexus

Nexus is an AI-native development environment organized around four user-facing concepts: `project`, `branch`, `version`, and `environment`.

## Mental Model

- `project`: list and select the repository-level context for your work.
- `branch`: select the active development branch context.
- `version`: reserved command group for upcoming product version workflows.
- `environment`: create and operate isolated development environments.

## Quick Start

### Installation

```bash
# macOS
brew install nexus

# Linux
curl -fsSL https://nexus.dev/install.sh | bash
```

### First Workflow (5 minutes)

```bash
# Create and enter an environment
nexus environment create demo-env
nexus environment ssh demo-env
```

Project and branch command groups are present in the interface model, but `nexus project list` and `nexus branch use <name>` are currently scaffold stubs that return not-implemented errors.

## User Guides

- [Environment Quickstart](tutorials/environment-quickstart.md)
- [CLI Reference](reference/nexus-cli.md)
- [Examples](examples/README.md)

## Examples

- [Quickstart](examples/quickstart/)
- [Node + React](examples/node-react/)
- [Python + Django](examples/python-django/)
- [Go Microservices](examples/go-microservices/)
- [Fullstack + PostgreSQL](examples/fullstack-postgres/)
- [Remote Server](examples/remote-server/)

## For Developers

- [Contributing](dev/contributing.md)
- [Roadmap](dev/roadmap.md)
- [Internal Docs](dev/)
