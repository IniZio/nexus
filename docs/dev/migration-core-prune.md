# Migration: Core Prune

Nexus has been hard-pruned to a workspace-core scope.

## Removed surfaces

- Enforcer/Boulder packages and workflows
- IDE/plugin package surfaces outside workspace core
- Non-core docs referencing removed modules

## Current supported surfaces

- `packages/nexus`
- `packages/sdk/js`

## What to update in downstream usage

- Stop referencing removed package paths in scripts/automation.
- Use workspace daemon + sdk references in documentation and CI.
- For project configuration, use `.nexus/workspace.json` and workspace reference docs.
