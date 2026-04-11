# Nexus

Nexus is a remote workspace runtime for strong VM isolation with fast local development loops.

## Key Capabilities

- Firecracker microVM isolation for a stronger boundary than typical process sandboxes.
- Docker inside the workspace VM by default for service-based app stacks.
- Mutagen-based sync between host and VM so local work survives VM failure/corruption.
- `nexus tunnel <workspace-id>` for manual testing against forwarded compose ports.
- Auto-install/runtime bootstrap and auth-forward flows for opencode/codex/claude tooling.
- On macOS without nested virtualization support (common on some Apple Silicon hosts), automatic seatbelt fallback.

## Install (one line)

```bash
curl -fsSL https://raw.githubusercontent.com/inizio/nexus/main/install.sh | bash
```

Then verify:

```bash
nexus --help
```

Prefer binaries instead? Use [GitHub releases](https://github.com/IniZio/nexus/releases).

## Quick Start

From your project root:

```bash
nexus init
nexus create
nexus list
nexus start <workspace-id>
```

`nexus create` prints the workspace id (for example: `created workspace my-repo (id: ws-...)`).

Common operations:

```bash
nexus start <workspace-id>
nexus tunnel <workspace-id>
nexus ssh <workspace-id>
nexus stop <workspace-id>
nexus remove <workspace-id>
```

Other operations:

```bash
nexus fork --id <workspace-id> --name <child-name>
```

`tunnel` is blocking; press Ctrl-C to close created tunnels.

## Docs

- Start here: `docs/index.md`
- Installation details: `docs/tutorials/installation.md`
- CLI reference: `docs/reference/cli.md`
- SDK reference: `docs/reference/sdk.md`
- Project structure: `docs/reference/project-structure.md`
- Workspace config: `docs/reference/workspace-config.md`

