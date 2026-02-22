# Nexus Workspace Guide: Docker Compose Multi-Service

This guide explains how to use this full-stack example with Nexus workspaces using the seamless workflow.

## Quick Start

### 1. Create and Activate the Workspace

```bash
# Create workspace
nexus workspace create fullstack-demo

# Activate workspace (enables auto-intercept)
nexus workspace use fullstack-demo
```

### 2. Start the Services

With the workspace active, work normally—no prefixes needed:

```bash
# Navigate to the example
cd examples/docker-compose-workspace

# Start all services
docker-compose up -d

# Verify services are running
docker-compose ps
```

### 3. Access the Application

The services run inside the workspace container:

- **Frontend**: http://localhost:5173
- **API**: http://localhost:3000
- **API Health**: http://localhost:3000/health

## Daily Development Workflow

```bash
# 1. Activate workspace
nexus workspace use fullstack-demo

# 2. Navigate to project
cd examples/docker-compose-workspace

# 3. Start services
docker-compose up -d

# 4. Work normally - no nexus prefix needed!
npm install
npm run dev
docker-compose logs -f api
```

## Escaping to Host

Need to run on your host machine? Use the `HOST:` prefix:

```bash
# Install a global tool on host
HOST:npm install -g typescript

# Check host Docker
HOST:docker ps

# Check main repo
HOST:git status
```

## Deactivating

```bash
# Return to host - commands run locally again
nexus workspace use --clear

# Or check current status
nexus workspace status
```

## Example Session

```bash
# Activate workspace
nexus workspace use fullstack-demo

# Navigate and start services
cd examples/docker-compose-workspace
docker-compose up -d

# Install a dependency in the workspace
npm install lodash

# Run tests
npm test

# Check workspace status
nexus workspace status

# Done for the day - deactivate
nexus workspace use --clear
```

## Troubleshooting

### Port Already in Use

```bash
# Check what's using the port
HOST:lsof -i :5173

# Or change the port in docker-compose.yml
```

### Docker Issues

```bash
# Check workspace status
nexus workspace status

# View docker-compose logs
docker-compose logs
```

## See Also

- [Quickstart Tutorial](../tutorials/workspace-quickstart.md)
- [CLI Reference](../reference/nexus-cli.md)
- [Main README](./README.md)
