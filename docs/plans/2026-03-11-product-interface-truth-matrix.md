# Product Interface Truth Matrix (2026-03-11)

## Scope and Method

- Scope: user-facing docs in `docs/index.md`, `docs/tutorials/`, `docs/reference/`, `docs/explanation/`, and `docs/examples/`.
- Truth sources: Cobra command registration in `packages/nexusd/internal/cli/root.go` and `packages/nexusd/internal/cli/workspace.go`, plus help output from:
  - `go run ./cmd/cli --help` (from `packages/nexusd`)
  - `go run ./cmd/cli workspace --help` (from `packages/nexusd`)
  - `go run ./cmd/cli workspace checkpoint --help` (from `packages/nexusd`)
- Additional cross-checks: `packages/nexusd/internal/cli/status.go`, `packages/nexusd/internal/cli/sync.go`, and `packages/nexusd/internal/cli/telemetry.go`.

## Implemented CLI Surface (Current)

### Root commands (implemented)

- `nexus boulder`
- `nexus completion`
- `nexus config`
- `nexus doctor`
- `nexus status`
- `nexus sync`
- `nexus trace`
- `nexus version`
- `nexus workspace`

### Workspace commands (implemented)

- `nexus workspace checkpoint`
  - `create`
  - `delete`
  - `list`
  - `restore`
- `nexus workspace create`
- `nexus workspace delete`
- `nexus workspace exec`
- `nexus workspace inject-key`
- `nexus workspace list`
- `nexus workspace logs`
- `nexus workspace ssh`
- `nexus workspace start`
- `nexus workspace status`
- `nexus workspace stop`
- `nexus workspace use`

## Truth Matrix

| Area | File | Current wording/claim | Verified implementation source | Status | Required action |
|---|---|---|---|---|---|
| Product model framing | `docs/index.md` | Workspace is primary user interface; no clear future canonical model note | `packages/nexusd/internal/cli/root.go`, `workspace.go` | accurate | Keep workspace-first for now; add short internal/future terminology note without implying shipped `org/project/...` CLI |
| Telemetry status | `docs/index.md` | Telemetry marked planned | `packages/nexusd/internal/cli/root.go` (`trace` command), `telemetry.go` | mismatched | Mark telemetry as implemented while avoiding over-claims about completed product surfaces |
| Install verification | `docs/tutorials/installation.md` | Uses `nexus --version` and workspace list checks; generally workspace-first | `go run ./cmd/cli --help`, `root.go`, `workspace.go` | accurate | Keep but tighten examples to real command behavior in later task |
| Workspace quickstart semantics | `docs/tutorials/workspace-quickstart.md` | Claims universal command auto-intercept and shows `nexus workspace status` without workspace arg | `workspace.go` (`use` messaging), `workspace.go` (`status <name>` requires one arg) | overstated | Narrow auto-intercept claims; fix `workspace status` usage to `status <name>` in later task |
| CLI reference command set | `docs/reference/nexus-cli.md` | Documents `--dind`; omits many implemented commands; shows `workspace status` without `<name>` | `workspace.go`, workspace help output | mismatched | Rewrite command reference from source/help; remove unsupported flags; include implemented `checkpoint` commands |
| Daemon package path | `docs/reference/workspace-daemon.md` | References `packages/workspace-daemon/` and `workspace-daemon` binary/product names | Repo tree + `packages/nexusd/`, `packages/nexusd/cmd/daemon/main.go` | mismatched | Rewrite against `packages/nexusd` reality or narrow to truthful internal note |
| Public SDK availability | `docs/reference/workspace-sdk.md` | Documents `@nexus/workspace-sdk` as installable/public | Repo tree (no such package) | future-only | Replace with truthful note that public SDK is not currently shipped |
| User-facing architecture | `docs/explanation/architecture.md` | Workspace/daemon architecture described; no future-interface boundary note | `root.go`, `workspace.go`, `status.go`, `sync.go` | accurate | Keep implemented architecture and add clearly labeled future-interface direction note |
| Examples landing | `docs/examples/README.md` | Workspace-first language | `workspace.go` | accurate | Keep workspace framing; ensure linked examples avoid unsupported commands |
| Quickstart example | `docs/examples/quickstart/README.md` | Workspace create/ssh/list flow | `workspace.go` | accurate | Keep with minor truth checks if needed |
| Remote server example | `docs/examples/remote-server/README.md` | Uses `nexus workspace port add`, `nexus auth login`, `nexus update` | `workspace.go`, `root.go` help output (no `port`, `auth`, `update`) | mismatched | Remove/replace unsupported commands; keep to implemented workspace/sync/status paths |
| Node + React example | `docs/examples/node-react/README.md` | Uses `nexus workspace create --dind` and `workspace port add` | `workspace.go` (`create` has no `--dind`; no `port`) | mismatched | Replace with supported flags/flows only |
| Python + Django example | `docs/examples/python-django/README.md` | Uses `workspace create --dind` and `workspace port add` | `workspace.go` | mismatched | Replace unsupported options/commands |
| Go microservices example | `docs/examples/go-microservices/README.md` | Uses `workspace create --dind` and `workspace port add` | `workspace.go` | mismatched | Replace unsupported options/commands |
| Fullstack + Postgres example | `docs/examples/fullstack-postgres/README.md` | Uses `workspace create --dind` and `workspace port add` | `workspace.go` | mismatched | Replace unsupported options/commands |

## Required Migration Notes (Explicit)

- User docs are currently workspace-first, while approved canonical design is project-first (`Org -> Project -> Branch -> Version -> Environment`). User docs must remain truthful to shipped CLI until new commands exist.
- `Box` is internal-only and must not appear as a user primitive in user-facing docs.
- `docs/reference/workspace-daemon.md` references `packages/workspace-daemon/`, which is not present in this repository.
- `docs/reference/workspace-sdk.md` references `@nexus/workspace-sdk`, which is not present in this repository.
