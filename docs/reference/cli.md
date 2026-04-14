# CLI Reference

Direct control for creating, starting, and accessing isolated workspaces.

## Install and first run

```bash
curl -fsSL https://raw.githubusercontent.com/inizio/nexus/main/install.sh | bash
cd /path/to/project
nexus init && nexus create && nexus shell <id>
```

`nexus create` prints the workspace id used by every subsequent command.

## Commands

### Workspace lifecycle

```
nexus create [--backend firecracker]
```
Creates a workspace from the current directory. Reads host credentials automatically (git config, SSH keys) and forwards them into the workspace. Optional `--backend` overrides runtime selection.

```
nexus list
```
Lists all workspaces with id, name, state, backend, and local worktree path.

```
nexus start <id>
nexus stop <id>
nexus remove <id>
nexus restore <id>
```
Start a stopped workspace, stop a running one, or permanently remove it. `restore` restores from the last snapshot.

```
nexus fork <id> <name> [--ref <ref>]
```
Forks a workspace into a new branch. `<name>` becomes the workspace name and, by default, the git ref. Use `--ref` to specify a different ref.

### Execution

```
nexus shell <id> [--timeout <dur>]
```
Opens an interactive bash session in the workspace via PTY. Optional `--timeout` sets a max wall time (e.g. `90s`). Auth relay token read from `$NEXUS_AUTH_RELAY_TOKEN` when set.

```
nexus exec <id> [--timeout <dur>] -- <command> [args...]
```
Runs a single non-interactive command in the workspace and streams output. The `--` separator is required. Auth relay token read from `$NEXUS_AUTH_RELAY_TOKEN` when set.

```
nexus run [--backend <name>] [--timeout <dur>] -- <command> [args...]
```
Creates an ephemeral workspace from the current directory, runs the command, then removes the workspace. Exit code matches the command's. Useful for one-off jobs that should leave no state behind.

### Port forwarding

```
nexus tunnel <id>
```
Applies compose-defined port forwards for the workspace and blocks until Ctrl-C, then closes them. Useful in CI pipelines where a compose project needs ports surfaced to the host.

### Maintenance

```
nexus init [path] [--force]
```
Scaffolds `.nexus/workspace.json` and `lifecycles/` in the target directory (defaults to cwd). `--force` overwrites existing files.

```
nexus doctor [--report-json <path>]
```
Runs health checks on the local runtime environment and prints a report. Optional `--report-json` writes the full result as JSON.

```
nexus version [--json]
```
Prints current CLI version, running daemon version (if reachable), latest release version, and updater status.

```
nexus update [--check] [--force] [--rollback] [--json]
```
Checks latest release metadata and applies updates for both `nexus` and `nexus-daemon`. Use `--check` for read-only status and `--rollback` to revert to the previous installed binaries.

## Environment variables

| Variable | Description |
|---|---|
| `NEXUS_DAEMON_PORT` | Daemon port override (default `7874`) |
| `NEXUS_DAEMON_TOKEN` | Auth token override (auto-managed when unset) |
| `NEXUS_AUTH_RELAY_TOKEN` | Relay token for `shell` / `exec` commands |
| `NEXUS_RELEASE_BASE_URL` | Release asset base URL override for updater |
| `NEXUS_RELEASE_CHANNEL` | Release channel (`stable` default, `prerelease` to track latest prerelease tag) |
| `NEXUS_RELEASE_REPO` | GitHub repo slug for release lookup (default `inizio/nexus`) |

## Related

- SDK: [`sdk.md`](sdk.md)
- Workspace config: [`workspace-config.md`](workspace-config.md)
