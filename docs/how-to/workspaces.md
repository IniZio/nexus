# Workspaces

Workspaces in Nexus provide isolated environments for managing tasks and projects.

## Creating a Workspace

To create a new workspace, use the `nexus workspace create` command:

```bash
nexus workspace create my-workspace
```

## Managing Workspaces

List all workspaces:
```bash
nexus workspace list
```

Switch to a workspace:
```bash
nexus workspace use my-workspace
```

Delete a workspace:
```bash
nexus workspace delete my-workspace
```

## Best Practices

- Use descriptive names for workspaces
- Keep workspaces focused on specific projects or goals
- Regularly clean up unused workspaces
