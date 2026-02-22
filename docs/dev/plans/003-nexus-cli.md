# Nexus CLI PRD

**Status:** Draft  
**Created:** 2026-02-22  
**Component:** CLI  

---

## 1. Overview

### 1.1 Problem Statement

Currently, Nexus functionality is scattered across multiple entry points:
- `boulder` CLI for enforcement operations
- IDE plugins (OpenCode, Claude Code, Cursor) for IDE integration
- Workspace operations require direct daemon interaction

This fragmentation creates:
- Users must remember multiple commands and tools
- Inconsistent command patterns across interfaces
- Difficult to script and automate workflows
- No single source of truth for Nexus operations

### 1.2 Goals

1. **Unified Interface**: Single `nexus` CLI replacing all existing entry points
2. **Consistent UX**: Docker/kubectl-style subcommand structure
3. **Interactive Mode**: TUI for real-time workspace monitoring
4. **Scripting Support**: JSON output, idempotent operations, exit codes
5. **Auto-Updates**: Seamless version updates

### 1.3 Non-Goals

- Replace IDE plugins (they remain as primary IDE integration)
- Support for remote workspaces via SSH (Phase 2)
- Web dashboard (separate project)
- Multi-user server mode (local-only CLI)

---

## 2. Architecture

### 2.1 Command Hierarchy

```
nexus
├── workspace
│   ├── create <name> [options]
│   ├── start <name>
│   ├── stop <name>
│   ├── delete <name>
│   ├── list
│   ├── ssh <name>
│   └── exec <name> -- <command>
├── boulder
│   ├── status
│   ├── pause
│   ├── resume
│   └── config
├── trace
│   ├── list
│   ├── show <id>
│   └── export <id>
├── config
│   ├── get [key]
│   ├── set <key> <value>
│   └── edit
├── status
├── doctor
└── version
```

### 2.2 Global Options

| Flag | Description | Default |
|------|-------------|---------|
| `--config <path>` | Config file location | `~/.nexus/config.yaml` |
| `--verbose, -v` | Debug output | false |
| `--json` | JSON output | false |
| `--quiet, -q` | Minimal output | false |

### 2.3 Exit Codes

| Code | Description |
|------|-------------|
| 0 | Success |
| 1 | General error |
| 2 | Invalid arguments |
| 3 | Workspace not found |
| 4 | Workspace already exists |
| 5 | Daemon not running |

---

## 3. Workspace Commands

### 3.1 Create Workspace

```bash
nexus workspace create <name> [options]

Options:
  -t, --template    Template (node, python, go, rust, blank)
  -f, --from        Import from existing project path
  --cpu             CPU limit (cores) [default: 2]
  --memory          Memory limit (GB) [default: 4]
```

### 3.2 Lifecycle Commands

```bash
nexus workspace start <name>
nexus workspace stop <name> [--force]
nexus workspace delete <name> [--force]
```

### 3.3 List Workspaces

```bash
nexus workspace list [--all] [--format table|json]

Output:
NAME           STATUS    CPU   MEM   DISK   CREATED
myproject      running   2     4GB   20GB   2026-02-22
api-service    stopped   2     4GB   20GB   2026-02-20
```

### 3.4 SSH Access

```bash
# Interactive SSH shell
nexus workspace ssh <name>

# Execute command
nexus workspace exec <name> -- npm test
```

---

## 4. Boulder Integration

### 4.1 Status

```bash
nexus boulder status [--verbose]

Output:
Boulder Enforcer Status
───────────────────────
Global Status: active
Active Workspaces: 3
Idle Detection: enabled
Workflow Enforcement: enabled
```

### 4.2 Control

```bash
nexus boulder pause [--workspace <name>]
nexus boulder resume [--workspace <name>]
```

### 4.3 Configuration

```bash
nexus boulder config get <key>
nexus boulder config set <key> <value>
```

---

## 5. Trace Commands

```bash
# List traces
nexus trace list [--limit N] [--from DATE]

# Show trace
nexus trace show <trace_id> [--spans] [--attribution]

# Export trace
nexus trace export <trace_id> --format FORMAT --output FILE

# Delete old traces
nexus trace prune [--older-than DAYS]
```

---

## 6. Configuration

### 6.1 Configuration File

**Location:** `~/.nexus/config.yaml`

```yaml
version: 1

workspace:
  default: myproject
  auto_start: true
  storage_path: ~/.nexus/workspaces

boulder:
  enforcement_level: normal
  idle_threshold: 30

telemetry:
  enabled: true
  sampling: 100
  retention_days: 30

daemon:
  host: localhost
  port: 9847

cli:
  update:
    auto_install: true
    channel: stable
```

### 6.2 Configuration Commands

```bash
nexus config get [key]
nexus config set <key> <value>
nexus config edit
nexus config init [--force]
```

---

## 7. Implementation Plan

### Phase 1: Core CLI (Week 1-2)
- [ ] Set up Oclif project structure
- [ ] Implement basic command framework
- [ ] Add global flags (--json, --verbose, --config)
- [ ] Implement `nexus config` commands
- [ ] Implement `nexus version` and `nexus status`

### Phase 2: Workspace Commands (Week 3-4)
- [ ] Implement `nexus workspace create`
- [ ] Implement `nexus workspace list`
- [ ] Implement `nexus workspace start/stop`
- [ ] Implement `nexus workspace ssh`
- [ ] Implement `nexus workspace exec`

### Phase 3: Boulder Commands (Week 5)
- [ ] Implement `nexus boulder status`
- [ ] Implement `nexus boulder pause/resume`
- [ ] Implement `nexus boulder config`

### Phase 4: Telemetry Commands (Week 6)
- [ ] Implement `nexus trace list`
- [ ] Implement `nexus trace show`
- [ ] Implement `nexus trace export`

### Phase 5: Polish & Release (Week 7-8)
- [ ] Auto-update integration
- [ ] Completion scripts (bash, zsh, fish)
- [ ] Migration documentation
- [ ] Deprecation warnings for boulder CLI

---

## 8. Migration from Boulder CLI

### 8.1 Command Mapping

| Boulder Command | Nexus Command |
|----------------|---------------|
| `boulder status` | `nexus boulder status` |
| `boulder pause` | `nexus boulder pause` |
| `boulder resume` | `nexus boulder resume` |
| `boulder config get` | `nexus boulder config get` |

### 8.2 Deprecation Timeline

| Phase | Timeline | Action |
|-------|----------|--------|
| 1 | Launch | `nexus` available, `boulder` still works |
| 2 | +3 months | Deprecation warning on `boulder` |
| 3 | +6 months | `boulder` shows migration message |
| 4 | +12 months | `boulder` removed |

---

## 9. References

- [Oclif Documentation](https://oclif.io)
- [ADR-003: Telemetry Design](../decisions/003-telemetry-design.md)

---

**Last Updated:** February 2026
