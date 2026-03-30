# Workspace Project Config

Nexus project-level workspace behavior is configured by `.nexus/workspace.json`.

For docker-compose projects, most users do not need this file: Nexus auto-detects compose and forwards all published ports by convention.

## File location

- `.nexus/workspace.json` in project root

## Schema

Use raw GitHub schema URL until a Nexus website exists:

```json
{
  "$schema": "https://raw.githubusercontent.com/IniZio/nexus/main/schemas/workspace.v1.schema.json",
  "version": 1
}
```

## Precedence

Effective config resolution order:

1. explicit API request params
2. `.nexus/workspace.json`
3. built-in Nexus defaults

## Migration

- `.nexus/workspace.json` is the only supported project config.
- Legacy split config files are not part of the fast-path workflow.

## Minimal example

```json
{
  "$schema": "https://raw.githubusercontent.com/IniZio/nexus/main/schemas/workspace.v1.schema.json",
  "version": 1,
  "readiness": {
    "profiles": {
      "default-services": [
        {"name":"student-portal","type":"service","serviceName":"student-portal"},
        {"name":"api","type":"service","serviceName":"api"},
        {"name":"opencode-acp","type":"service","serviceName":"opencode-acp"}
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
      {"service":"student-portal","remotePort":5173,"localPort":5173},
      {"service":"api","remotePort":8000,"localPort":8000}
    ]
  },
  "auth": {
    "defaults": {
      "authProfiles": ["gitconfig"],
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

## Compose convention mode (recommended default)

Without any config file, Nexus will:

- detect `docker-compose.yml` or `docker-compose.yaml` in workspace root
- parse compose config
- auto-forward all published ports via Spotlight on `workspace.ready`

Use `.nexus/workspace.json` only when you need overrides (custom readiness profiles, service defaults, explicit spotlight defaults, auth defaults).
