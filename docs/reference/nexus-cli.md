# Nexus CLI Reference

This reference documents the currently implemented `nexus` CLI surface from `packages/nexusd/internal/cli` and live help output.

## Global Usage

```bash
nexus [command]
```

Global flags:
- `--config <path>`
- `--json`
- `-q, --quiet`
- `-v, --verbose`

Version check:

```bash
nexus version
```

## Top-Level Commands

- `nexus boulder`
- `nexus completion`
- `nexus config`
- `nexus doctor`
- `nexus status`
- `nexus sync`
- `nexus trace`
- `nexus version`
- `nexus workspace`

## Workspace Commands

```bash
nexus workspace [command]
```

Implemented subcommands:
- `checkpoint`
- `create`
- `delete`
- `exec`
- `inject-key`
- `list` (alias: `ls`)
- `logs`
- `ssh`
- `start`
- `status`
- `stop`
- `use`

### `nexus workspace create <name>`

Create a workspace.

```bash
nexus workspace create <name> [flags]
```

Flags:
- `--backend <docker|daytona>`
- `--cpu <int>` (default `2`)
- `--disk <int>` (default `20`)
- `--from <path>`
- `--memory <int>` (default `4`)

### `nexus workspace list`

List workspaces.

```bash
nexus workspace list [flags]
```

Flags:
- `--all`
- `--format <table|json>` (default `table`)

### `nexus workspace status <name>`

Show detailed status for one workspace.

```bash
nexus workspace status <name>
```

### `nexus workspace use [name]`

Set or clear active workspace for the current session metadata.

```bash
nexus workspace use <name>
nexus workspace use --clear
nexus workspace use -
```

Flag:
- `-c, --clear`

Note: `use` records active workspace context and prints guidance about host escape (`HOST:`). For deterministic command execution in a workspace, prefer `nexus workspace ssh <name>` or `nexus workspace exec <name> -- <command>`.

### `nexus workspace exec <name> -- <command>`

Execute a command in a workspace.

```bash
nexus workspace exec <name> -- <command>
```

### `nexus workspace ssh <name>`

Open an interactive SSH session to a workspace.

```bash
nexus workspace ssh <name>
```

### `nexus workspace inject-key <name>`

Inject your SSH key into a workspace.

```bash
nexus workspace inject-key <name>
```

### `nexus workspace start <name>` / `stop <name>` / `delete <name>` / `logs <name>`

Lifecycle and logs commands for an individual workspace.

```bash
nexus workspace start <name>
nexus workspace stop <name> [--force]
nexus workspace delete <name> [--force]
nexus workspace logs <name>
```

### `nexus workspace checkpoint`

Manage workspace checkpoints.

```bash
nexus workspace checkpoint [command]
```

Subcommands:
- `nexus workspace checkpoint create <workspace>`
- `nexus workspace checkpoint list <workspace>`
- `nexus workspace checkpoint restore <workspace> <checkpoint-id>`
- `nexus workspace checkpoint delete <workspace> <checkpoint-id>`

## Sync Commands

```bash
nexus sync [command]
```

Implemented subcommands:
- `flush`
- `list`
- `pause`
- `resume`
- `status`
