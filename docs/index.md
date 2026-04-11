# Nexus Docs

Nexus gives you isolated VM workspaces with fast local iteration.

## Start in 2 Minutes

```bash
curl -fsSL https://raw.githubusercontent.com/inizio/nexus/main/install.sh | bash
cd /path/to/project
nexus init
nexus create
nexus list
nexus start <workspace-id>
```

`nexus create` prints the workspace id used by `start`, `ssh`, `tunnel`, and `stop`.

## Most Important Capabilities

- Firecracker-first isolation for stronger workspace boundaries.
- Docker inside the workspace VM by default.
- Mutagen sync keeps host work safe even if a VM is reset or corrupted.
- `nexus tunnel` provides quick manual testing of forwarded compose ports.
- Auth-forward and tooling bootstrap for opencode/codex/claude workflows.
- On macOS hosts without nested virtualization, runtime falls back to seatbelt.

## Read By Goal

- Install and verify: `docs/tutorials/installation.md`
- Learn CLI quickly: `docs/reference/cli.md`
- Automate with JS/TS: `docs/reference/sdk.md`
- Understand project internals: `docs/reference/project-structure.md`
- Customize project behavior: `docs/reference/workspace-config.md`
- Understand architecture: `docs/explanation/architecture.md`
