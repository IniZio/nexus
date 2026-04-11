# Workspace Config

Nexus uses convention-over-configuration. Most projects only need `nexus init`.

## File Location

- `.nexus/workspace.json` in project root

## Supported Shape

```json
{
  "$schema": "./schemas/workspace.v1.schema.json",
  "version": 1
}
```

- `$schema` is optional.
- `version` is optional and defaults to `1`.
- Additional keys are not supported.

## What Is Configured by Convention

- Lifecycle scripts:
  - `.nexus/lifecycles/setup.sh`
  - `.nexus/lifecycles/start.sh`
  - `.nexus/lifecycles/teardown.sh`
- Doctor probes: executable scripts under `.nexus/probe/`
- Doctor checks: executable scripts under `.nexus/check/`
- Tunnel defaults: compose ports from `docker-compose.yml` or `docker-compose.yaml`

## Runtime Selection

Runtime is selected automatically:

- Linux: Firecracker-first
- macOS: Firecracker via Lima when available, otherwise seatbelt fallback

Project-level runtime overrides are intentionally not supported.

## Related Docs

- CLI: `docs/reference/cli.md`
- SDK: `docs/reference/sdk.md`
- Project structure: `docs/reference/project-structure.md`