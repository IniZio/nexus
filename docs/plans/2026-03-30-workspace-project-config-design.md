# Workspace Project Config Design

Date: 2026-03-30
Status: Proposed and approved in design review
Audience: Internal Nexus maintainers

## 1. Goal

Provide a single, repo-native configuration file that defines workspace lifecycle behavior, readiness profiles, service defaults, Spotlight defaults, and auth policy defaults for Nexus remote workspaces.

The design prioritizes one-file ergonomics for project maintainers and minimal API surface for Nexus callers.

## 2. Canonical File

- Path: `.nexus/workspace.json`
- Format: JSON only (v1)
- Single source of truth for project workspace behavior

## 3. Schema Self-Discovery

The config must support JSON schema tooling via `$schema`.

Until a Nexus website exists, use raw GitHub content URL for schema distribution.

Example:

```json
{
  "$schema": "https://raw.githubusercontent.com/IniZio/nexus/main/schemas/workspace.v1.schema.json",
  "version": 1
}
```

## 4. Configuration Shape (v1)

```json
{
  "$schema": "https://raw.githubusercontent.com/IniZio/nexus/main/schemas/workspace.v1.schema.json",
  "version": 1,
  "readiness": {
    "profiles": {
      "default-services": [
        { "name": "student-portal", "type": "service", "serviceName": "student-portal" },
        { "name": "api", "type": "service", "serviceName": "api" },
        { "name": "opencode-acp", "type": "service", "serviceName": "opencode-acp" }
      ]
    }
  },
  "services": {
    "defaults": {
      "stopTimeoutMs": 1500,
      "autoRestart": false,
      "maxRestarts": 1,
      "restartDelayMs": 250
    }
  },
  "spotlight": {
    "defaults": [
      { "service": "student-portal", "remotePort": 5173, "localPort": 5173 },
      { "service": "api", "remotePort": 8000, "localPort": 8000 }
    ]
  },
  "auth": {
    "defaults": {
      "authProfiles": ["claude-auth", "codex-auth"],
      "sshAgentForward": true,
      "gitCredentialMode": "host-helper"
    }
  },
  "lifecycle": {
    "onSetup": ["pnpm install"],
    "onStart": ["opencode serve"],
    "onTeardown": []
  }
}
```

## 5. One-File Direction

Nexus uses one canonical project file:

- `workspace.json` is the canonical source.
- Legacy split lifecycle config is intentionally not part of the fast path.

## 6. Resolution Precedence

When deriving effective settings:

1. Explicit API request params
2. `.nexus/workspace.json`
3. Nexus built-in defaults

This keeps runtime behavior overridable while preserving project defaults.

## 7. Compatibility Stance

Fast-break policy:

- `.nexus/workspace.json` is required for project-level lifecycle/readiness/service/spotlight defaults.
- No legacy fallback behavior in the main path.

## 8. Validation Rules

- Validate `workspace.json` against JSON Schema v1.
- Missing file is non-fatal (use built-in defaults).
- Invalid file is fatal for config-dependent operations, with clear path + field errors.

## 9. Integration Points

### `workspace.create`

- Apply `auth.defaults` and `services.defaults` unless explicit request overrides exist.

### `workspace.ready`

- Resolve profile from `readiness.profiles` in `workspace.json` first.
- Fallback to built-in profile map if not present.

### Service commands

- `service.command` defaults (`start`, `restart`, `stop`) derive from `services.defaults`.

### Spotlight

- Add optional action to apply `spotlight.defaults` in one call (or helper in SDK).

### Lifecycle manager

- Read `lifecycle` block from `workspace.json`.

## 10. Non-Goals (v1)

- YAML/TOML support
- External config registries
- Includes/imports across config files
- Project-specific hardcoded names in Nexus public docs

## 11. Developer Experience

- One file to edit for project setup/start/readiness.
- IDE auto-completion and validation via `$schema`.
- Minimal required fields; sensible defaults for omitted sections.

## 12. Next Step

Create implementation plan to:

1. Add schema file and validator
2. Add daemon config loader + cache
3. Integrate `workspace.json` across lifecycle/readiness/service/spotlight
4. Keep strict one-file behavior and remove fallback assumptions
5. Update SDK helpers/docs/tests
