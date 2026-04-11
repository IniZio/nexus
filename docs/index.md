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
| Install | [`docs/tutorials/installation.md`](tutorials/installation.md) |
| CLI | [`docs/reference/cli.md`](reference/cli.md) |
| JS/TS SDK | [`docs/reference/sdk.md`](reference/sdk.md) |
| `.nexus` config | [`docs/reference/workspace-config.md`](reference/workspace-config.md) |
