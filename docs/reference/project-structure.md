# Project Structure

Nexus keeps project integration intentionally small: one directory, clear roles.

## Repository layout (high level)

```text
docs/
├── README.md
├── guides/
├── superpowers/
│   └── plans/
├── reference/
└── roadmap.md

packages/
├── e2e/
│   └── flows/
│       └── src/
│           ├── cases/
│           │   └── parity/
│           ├── harness/
│           │   ├── daemon/
│           │   ├── repo/
│           │   ├── session/
│           │   └── assertions/
│           └── parity/
├── nexus/          # Go daemon
├── nexus-ui/       # Web UI
└── sdk/
    └── js/         # TypeScript SDK (@nexus/sdk)
```


## Minimal Structure

Canonical project scaffold lives at the **repository root** as `.nexus/` (used by `nexus init` and `nexus doctor`). Do not duplicate it under package directories.

```text
.nexus/                 # at repo root only
  workspace.json
  lifecycles/
    setup.sh
    start.sh
    teardown.sh
  probe/
  check/
```

## Mental Model

- `workspace.json`: schema/version marker.
- `lifecycles/`: setup, start, and teardown hooks.
- `probe/`: environment and runtime probes.
- `check/`: behavioral checks used by `nexus doctor`.

If these files are present and executable where needed, Nexus can infer most behavior without extra config.

## Common Commands

```bash
nexus init
nexus doctor --suite local
nexus tunnel <workspace-id>
```

## Related Docs

- Workspace config: `docs/reference/workspace-config.md`
- CLI: `docs/reference/cli.md`
- SDK: `docs/reference/sdk.md`
- Architecture: `CONTRIBUTING.md` (repository root)
