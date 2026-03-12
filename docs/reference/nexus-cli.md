# Nexus CLI Reference

This page documents the currently implemented `nexus` CLI commands from live help output in `packages/nexusd/cmd/cli`.

## Mental Model

- `project`: repository-level context
- `branch`: active branch context
- `version`: reserved command group for upcoming version workflows
- `environment`: isolated runtime for development work

## Global Usage

```bash
nexus [command]
```

Global flags:
- `--config <path>` (default `~/.nexus/config.yaml`)
- `--json`
- `-q, --quiet`
- `-v, --verbose`

## Top-Level Commands

Implemented root commands (from `Available Commands`):
- `nexus boulder`
- `nexus branch`
- `nexus cli-version`
- `nexus completion`
- `nexus config`
- `nexus doctor`
- `nexus environment`
- `nexus project`
- `nexus status`
- `nexus sync`

Built-in help command:
- `nexus help`

Additional help topics (from `Additional help topics`):
- `nexus version`

## Project Commands

```bash
nexus project [command]
```

Scaffold subcommands (present in help, currently return not-implemented errors):
- `list`

Scaffold preview:

```bash
nexus project list
```

## Branch Commands

```bash
nexus branch [command]
```

Scaffold subcommands (present in help, currently return not-implemented errors):
- `use`

Scaffold preview:

```bash
nexus branch use <name>
```

## Version Help Topic

```bash
nexus version [command]
```

Current state:
- `version` is currently exposed as a help topic (reserved group), not an implemented root command
- no user-facing subcommands are exposed yet

## Environment Commands

```bash
nexus environment [command]
```

Implemented subcommands:
- `checkpoint`
- `create`
- `delete`
- `exec`
- `inject-key`
- `list`
- `ls` (alias of `list`)
- `logs`
- `ssh`
- `start`
- `status`
- `stop`
- `use`

### `nexus environment create <name>`

Create a new environment.

```bash
nexus environment create <name> [flags]
```

Flags:
- `--backend <docker|daytona>`
- `--cpu <int>` (default `2`)
- `--disk <int>` (GB, default `20`)
- `--from <path>`
- `--memory <int>` (GB, default `4`)

### `nexus environment status <name>`

Show detailed status for one environment.

```bash
nexus environment status <name>
```

### `nexus environment exec <name> -- <command>`

Execute a command in an environment.

```bash
nexus environment exec <name> -- <command>
```

### `nexus environment use [name]`

Set active environment context for supported commands.

```bash
nexus environment use <name>
nexus environment use --clear
```

### Lifecycle and Logs

```bash
nexus environment start <name>
nexus environment stop <name>
nexus environment delete <name>
nexus environment logs <name>
```

### Checkpoints

```bash
nexus environment checkpoint create <environment>
nexus environment checkpoint list <environment>
nexus environment checkpoint restore <environment> <checkpoint-id>
nexus environment checkpoint delete <environment> <checkpoint-id>
```

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

Examples:

```bash
nexus sync list
nexus sync status [environment]
```
