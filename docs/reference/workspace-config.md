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

Native Firecracker requires kernel and rootfs images, plus the Firecracker binary.

**Install Firecracker:**

Download the latest release from [github.com/firecracker-microvm/firecracker/releases](https://github.com/firecracker-microvm/firecracker/releases) or use your distribution's package manager if available:

```bash
# Example: download official release (check for latest version)
curl -L https://github.com/firecracker-microvm/firecracker/releases/download/v1.7.0/firecracker-v1.7.0-x86_64.tgz | tar xz
sudo mv firecracker-v1.7.0-x86_64/firecracker /usr/local/bin/
```

**Obtain kernel and rootfs:**

Kernel and rootfs images are not provided by Nexus. You must build or obtain compatible images:

- **Kernel**: Build from Linux source with Firecracker config, or use pre-built microvm kernels
- **Rootfs**: Create an ext4 filesystem with your target environment and the `nexus-firecracker-agent` binary installed at `/usr/local/bin/nexus-firecracker-agent`

See [Firecracker documentation](https://github.com/firecracker-microvm/firecracker/blob/main/docs/getting-started.md) for building kernel and rootfs images.

**Configure environment:**

```bash
export NEXUS_FIRECRACKER_KERNEL=/var/lib/nexus/vmlinux.bin
export NEXUS_FIRECRACKER_ROOTFS=/var/lib/nexus/rootfs.ext4
```

**Required environment variables:**

| Variable | Description |
|----------|-------------|
| `NEXUS_FIRECRACKER_KERNEL` | Path to Firecracker kernel image (required) |
| `NEXUS_FIRECRACKER_ROOTFS` | Path to Firecracker rootfs image (required) |

**Removed environment variables (no longer supported):**

| Variable | Status |
|----------|--------|
| `NEXUS_DOCTOR_FIRECRACKER_EXEC_MODE` | Removed in native firecracker cutover |
| `NEXUS_DOCTOR_FIRECRACKER_INSTANCE` | Removed in native firecracker cutover |
| `NEXUS_DOCTOR_FIRECRACKER_DOCKER_MODE` | Removed in native firecracker cutover |

Operational guardrails:

- keep ballooning disabled by default
- enforce memory ceilings through Firecracker machine configuration
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
