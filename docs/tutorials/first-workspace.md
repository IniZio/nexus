# Your First Workspace

## Initialize Nexus

```bash
cd your-project
nexus init
```

This creates a `.nexus/` directory with configuration, hooks, and workspace storage.

## Create a Workspace

```bash
# List available templates
nexus template list

# Create workspace with template
nexus workspace create feature-x --template node-postgres
```

Output:
```
🚀 Creating workspace 'feature-x'...
📁 Creating git worktree at .nexus/worktrees/feature-x/
🌿 Creating branch nexus/feature-x
📦 Applying template node-postgres...
🐳 Creating container...
✅ Workspace feature-x created (SSH port: 32777)
```

## Start the Workspace

```bash
nexus workspace up feature-x
```

## Check Services

```bash
nexus workspace ports feature-x
```

Output:
```
📦 Port mappings for feature-x:
  web:       3000 → 32778
  api:       5000 → 32779
  postgres:  5432 → 32780
```

## Access the Workspace

```bash
ssh -p 32777 -i ~/.ssh/id_ed25519_nexus dev@localhost
```

## Work on Your Feature

```bash
# Switch to workspace branch
git checkout nexus/feature-x

# Make changes
# ...

# Push changes
git push origin nexus/feature-x
```

## Next Steps

- [Debug Ports](../how-to/debug-ports.md)
- [CLI Reference](../reference/cli.md)
