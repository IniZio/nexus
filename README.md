# Nexus

Remote workspace runtime: strong VM isolation with fast local dev loops.

## Capabilities

- Firecracker microVM isolation; Docker in the workspace VM by default.
- Mutagen sync between host and VM.
- `nexus tunnel <workspace-id>` for forwarded compose ports.
- Tooling bootstrap and auth-forward for common AI coding tools.
- On macOS without nested virtualization, seatbelt fallback.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/inizio/nexus/main/install.sh | bash
nexus --help
```

Binaries: [GitHub releases](https://github.com/IniZio/nexus/releases).

## Quick start

```bash
nexus init
nexus create
nexus list
nexus start <workspace-id>
```

`nexus create` prints the workspace id. Common: `tunnel`, `ssh`, `stop`, `remove`; `fork --id <id> --name <child>`. `tunnel` blocks until Ctrl-C.

## Docs

| Topic | Path |
|--------|------|
| Hub | `docs/README.md` |
| Install | `docs/guides/installation.md` |
| CLI | `docs/reference/cli.md` |
| SDK | `docs/reference/sdk.md` |
| Project structure | `docs/reference/project-structure.md` |
| Config | `docs/reference/workspace-config.md` |
