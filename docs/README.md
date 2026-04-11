# Nexus Docs

Isolated VM workspaces with fast local iteration.

## Quick start

```bash
curl -fsSL https://raw.githubusercontent.com/inizio/nexus/main/install.sh | bash
cd /path/to/project
nexus init && nexus create && nexus list && nexus start <workspace-id>
```

`nexus create` prints the id used by `start`, `ssh`, `tunnel`, and `stop`.

## By goal


| Goal | Doc |
|------|-----|
| Install | [`docs/guides/installation.md`](guides/installation.md) |
| Operations (doctor, backends, paths) | [`docs/guides/operations.md`](guides/operations.md) |
| CLI | [`docs/reference/cli.md`](reference/cli.md) |
| JS/TS SDK | [`docs/reference/sdk.md`](reference/sdk.md) |
| Host auth bundle format (SDK / advanced) | [`docs/reference/host-auth-bundle.md`](reference/host-auth-bundle.md) |
| Project structure | [`docs/reference/project-structure.md`](reference/project-structure.md) |
| `.nexus` config | [`docs/reference/workspace-config.md`](reference/workspace-config.md) |
