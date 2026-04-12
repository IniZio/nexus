# Project Workspaces

Nexus now organizes workspaces under projects. A project represents a git repository, and workspaces within a project are isolated development environments for that codebase.

## Overview

- **Project**: Represents a git repository (e.g., `github.com/myorg/myrepo`)
- **Workspace**: An isolated development environment within a project, typically tied to a specific branch

Projects are created automatically when you run `nexus create` in a repository for the first time.

## Listing Workspaces

The `nexus list` command now shows workspaces grouped by project:

```bash
nexus list
```

Output:
```
PROJECT: myrepo (github.com/myorg/myrepo)
  main-branch      running    firecracker  main
  feature-xyz      stopped    seatbelt     feature/xyz

PROJECT: dotfiles (/Users/me/dotfiles)
  dotfiles-dev     running    seatbelt   master

2 projects, 3 workspaces total
```

Use `--flat` for the original flat listing:

```bash
nexus list --flat
```

## Managing Projects

### List all projects

```bash
nexus project list
```

### Show project details and workspaces

```bash
nexus project show <project-id>
```

### Remove a project

```bash
nexus project remove <project-id>
```

This removes the project and all its workspaces.

## Creating Workspaces

When you create a workspace with `nexus create`, it automatically:
1. Creates a project for the current repository (if it doesn't exist)
2. Creates the workspace linked to that project

```bash
cd ~/myrepo
nexus create  # Creates project (if new) + workspace
```

## Migration

Existing workspaces without projects are automatically migrated on daemon startup. Each workspace is assigned to a project based on its repository.
