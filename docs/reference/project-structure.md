# Project Structure

Nexus keeps project integration intentionally small: one directory, clear roles.

## Minimal Structure

```text
.nexus/
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
- Architecture: `docs/explanation/architecture.md`
