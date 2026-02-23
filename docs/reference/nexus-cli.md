# Nexus CLI Reference

## Workspace Commands

### nexus workspace create

Create a new Docker-based workspace.

```bash
nexus workspace create <name> [options]
```

**Options:**
- `--display-name <name>` - Human-readable display name
- `--dind` - Enable Docker-in-Docker support

**Example:**
```bash
nexus workspace create myproject
nexus workspace create fullstack-demo --dind
```

### nexus workspace use

Activate a workspace, enabling auto-intercept for all commands.

```bash
nexus workspace use <name>
nexus workspace use --clear
```

**Behavior:**
- When a workspace is active, commands automatically execute inside it
- No need to prefix commands with `nexus exec`
- The workspace context persists until cleared

**Examples:**
```bash
# Activate workspace
nexus workspace use myproject

# Commands now auto-route to workspace
docker-compose up -d
npm install
npm run dev

# Deactivate (return to host)
nexus workspace use --clear
```

### nexus workspace use --clear

Deactivate the current workspace context. Commands will run on the host machine.

```bash
nexus workspace use --clear
```

### nexus workspace list

List all available workspaces.

```bash
nexus workspace list
```

### nexus workspace status

Show the current workspace context.

```bash
nexus workspace status
```

Output includes:
- Active workspace name
- Connection status
- Available workspaces

## Auto-Intercept Behavior

When a workspace is active via `nexus workspace use`:

1. **Automatic Routing**: All commands execute inside the workspace
2. **Transparent**: No changes needed to your workflow
3. **Working Directory**: Commands run from the workspace's working directory

**Example workflow:**
```bash
nexus workspace use myproject

# These commands run IN the workspace
docker-compose up -d    # Workspace Docker daemon
npm install             # Workspace node_modules
npm run dev             # Dev server in workspace

# Need host instead? Use HOST: prefix
HOST:npm install -g some-tool
HOST:docker ps          # Host Docker
```

## HOST: Prefix

Escape the workspace context to run commands on the host machine.

```bash
HOST:<command> [args...]
```

**Use cases:**
- Install global tools on host
- Check host Docker containers
- Access the main repository
- Run host-specific tools

**Examples:**
```bash
HOST:npm install -g typescript
HOST:docker ps
HOST:git status
```
