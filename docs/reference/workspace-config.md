# Workspace Project Config

Nexus project-level workspace behavior is configured by `.nexus/workspace.json`.

For docker-compose projects, most users do not need this file: Nexus auto-detects compose and forwards all published ports by convention.

## File location

- `.nexus/workspace.json` in project root

## Schema

```json
{
  "$schema": "./schemas/workspace.v1.schema.json",
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
  "$schema": "./schemas/workspace.v1.schema.json",
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
    "onStart": [],
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

## Runtime Requirements

The `runtime` block declares isolated workspace backend constraints:

```json
{
  "version": 1,
  "runtime": {
    "required": ["firecracker"],
    "selection": "prefer-first"
  }
}
```

| Field | Type | Description |
|-------|------|-------------|
| `required` | `string[]` | Allowed backends: `firecracker`. Empty means daemon auto-selection from available capabilities. |
| `selection` | `string` | Backend selection strategy. `"prefer-first"` selects the first available from `required`. |

When no `runtime` block is present, Nexus selects a backend automatically.

### Firecracker Host Setup

Linux host prerequisites:

```bash
sudo apt-get update
sudo apt-get install -y firecracker
vmctl-firecracker --version
```

macOS host prerequisites (Lima path):

```bash
brew install lima qemu
limactl start --name=nexus-firecracker
limactl shell nexus-firecracker -- sudo apt-get update
limactl shell nexus-firecracker -- sudo apt-get install -y firecracker
limactl shell nexus-firecracker -- vmctl-firecracker --version
```

Operational guardrails:

- keep ballooning disabled by default (`--balloon off`)
- enforce memory ceilings through `vmctl-firecracker` options
- run lifecycle canary regularly: create -> pause -> fork -> resume -> destroy

## Capability Requirements

The `capabilities` block declares toolchain and runtime capability requirements:

```json
{
  "version": 1,
  "capabilities": {
    "required": ["spotlight.tunnel"]
  }
}
```

| Field | Type | Description |
|-------|------|-------------|
| `required` | `string[]` | Required capability names (e.g., `spotlight.tunnel`). Unknown capabilities cause workspace creation to fail. |

## Doctor Health Checks

`nexus doctor` runs two sequential phases:

1. **probes** — readiness and liveness checks. If any required probe fails, `tests` are skipped.
2. **tests** — behavioral and integration checks. Only run after all required probes pass.

```json
{
  "version": 1,
  "doctor": {
    "probes": [
      {
        "name": "runtime-http",
        "command": "bash",
        "args": [".nexus/lifecycles/probe-runtime.sh"],
        "timeoutMs": 300000,
        "retries": 1,
        "required": true
      }
    ],
    "tests": [
      {
        "name": "auth-flow",
        "command": "bash",
        "args": [".nexus/lifecycles/test-auth-flow.sh"],
        "timeoutMs": 300000,
        "retries": 0,
        "required": true
      }
    ]
  }
}
```

| Field | Type | Description |
|-------|------|-------------|
| `probes` | `DoctorCommandProbe[]` | Readiness/liveness checks. Run first. Required probe failure gates `tests`. |
| `tests` | `DoctorCommandCheck[]` | Behavioral/integration checks. Only run after required probes pass. |

### DoctorCheck fields

| Field | Type | Description |
|-------|------|-------------|
| `name` | `string` | Unique check name used in reports. |
| `command` | `string` | Executable path or name. |
| `args` | `string[]` | Arguments passed to `command`. |
| `timeoutMs` | `number` | Per-check timeout in milliseconds. Default: 60000. |
| `retries` | `number` | Retry count on failure. Default: 0. |
| `required` | `boolean` | If `true`, failure causes doctor to return a non-zero exit code. Default: `false`. |

CLI helpers:

- `--report-json .nexus/run/doctor-report.json` writes structured phase results (`"phase": "probe"` or `"phase": "test"`) for CI artifact upload.

### Android / Maestro example (probes only)

```json
{
  "version": 1,
  "doctor": {
    "probes": [
      {
        "name": "android-maestro-pr",
        "command": "maestro",
        "args": ["test", "--include-tags=pull-request", "./.maestro/flows"],
        "timeoutMs": 900000,
        "retries": 0,
        "required": true
      }
    ]
  }
}
```
