---
type: master
feature_area: dev-workflow-overhaul
date: 2026-04-17
status: active
child_prds: []
---

# Dev Workflow Overhaul

## Overview

Nexus has completed a radical simplification: the architecture is now Firecracker-only (dropping Lima/process-sandbox-as-driver complexity). This PRD overhauls the documentation, `Taskfile.yml`, testing structure, and agent skills so that:

1. **External contributors** can onboard in <30 minutes with no prior Nexus knowledge.
2. **Release pipeline** is explicit: opencode → conventional commits → PRs.
3. **Documentation** reflects the simplified architecture precisely — no stale references to removed backends.
4. **Taskfile targets** are organized by persona (contributor, CI, release manager).
5. **Agent skills** (groundwork workflow) are explicitly integrated into the contributor journey.

## Architecture

### Current State (Post-Simplification)

```
packages/
├── nexus/           # Go daemon + CLI (firecracker-only runtime)
├── nexus-swift/      # macOS app (NexusApp) embedding the daemon
├── sdk/js/           # TypeScript SDK (@nexus/sdk)
└── e2e/flows/       # E2E test harness

docs/
├── guides/           # installation, operations, release-signing
├── reference/        # cli, sdk, workspace-config, host-auth-bundle, project-structure
├── superpowers/     # plans/
└── roadmap.md       # (absent — needs creation)

Taskfile.yml         # Unified dev tasks (needs reorganization)
AGENTS.md           # Agent guidelines (needs refresh)
CONTRIBUTING.md     # Contributing guide (needs refresh)
```

### Runtime Architecture

The daemon runs on a **remote Linux** machine. Clients (CLI, SDK, macOS app) connect via JSON-RPC over WebSocket. The only supported VM backend is **Firecracker**. The `process` sandbox driver provides process-isolation fallback for environments where VMs are unavailable.

```
Client (macOS/Windows/CLI)
    │
    ▼  JSON-RPC / WebSocket
Daemon (Go, remote Linux + Firecracker)
    │
    ├── runtime/firecracker/   ← VM isolation (primary)
    └── runtime/sandbox/       ← process isolation (fallback)
```

Auth travels via `configBundle` in `workspace.create` RPC — no symlinks to daemon `$HOME`.

### File Size Policy

| Layer | Limit |
|---|---|
| Core/domain logic | ≤300 lines |
| Orchestration/application logic | ≤400 lines |
| Transport/adapters/tests | ≤500 lines |
| Generated files | exempt |

Known debt (tracked, not failures):
- `packages/nexus/pkg/handlers/workspace_manager.go` (~429 lines, over 400 limit)
- `packages/nexus/pkg/workspacemgr/manager.go` (~1269 lines, severely over limit — **priority refactor**)

## Documentation Structure

### New Docs Layout

```
docs/
├── README.md                      # Entry point — quick start, by-goal table
├── guides/
│   ├── installation.md           # install.sh, release binaries, from-source
│   ├── operations.md             # doctor, backends, paths
│   ├── release-signing.md         # signing keys
│   └── contributor-guide.md      # NEW: setup, build, test, PR workflow
├── reference/
│   ├── cli.md                    # nexus(1) full command reference
│   ├── sdk.md                    # TypeScript SDK usage
│   ├── workspace-config.md        # .nexus/workspace.json schema
│   ├── host-auth-bundle.md       # configBundle format for SDK/advanced
│   └── project-structure.md      # package layout, .nexus/ convention
├── superpowers/
│   └── plans/
└── roadmap.md                   # NEW: feature priorities, upcoming work
```

### Changes Required

| File | Action | Reason |
|---|---|---|
| `docs/README.md` | Update by-goal table | Add contributor-guide link |
| `docs/guides/contributor-guide.md` | **Create** | External contributor onboarding (this PRD) |
| `docs/roadmap.md` | **Create** | Feature priorities for contributors |
| `CONTRIBUTING.md` | Refresh | Align with new Taskfile targets and testing tiers |
| `AGENTS.md` | Refresh | Add known debt item for manager.go; confirm firecracker-only |
| `docs/reference/project-structure.md` | Update | Reflect removal of Lima; confirm firecracker-only |
| `docs/guides/installation.md` | Refresh | Remove any Lima references |
| `docs/guides/operations.md` | Refresh | Remove any Lima references |

### Contributor Guide Content (`contributor-guide.md`)

```
# Contributor Guide

## Prerequisites

- Go 1.21+
- Node.js 18+ and pnpm
- Xcode 15+ (macOS build)
- Docker (for runtime integration tests)

## Setup

```bash
git clone https://github.com/inizio/nexus.git
cd nexus
pnpm install
```

## Architecture Overview

Nexus has two core packages:

- `packages/nexus` — Go daemon exposing JSON-RPC over WebSocket; runs on remote Linux with Firecracker VMs
- `packages/sdk/js` — TypeScript SDK (`@nexus/sdk`) for client connectivity

Key constraint: **the daemon may run on a different machine than the user**. User credentials travel via `configBundle` in `workspace.create` RPC — never via symlinks to the daemon's `$HOME`.

## Building

```bash
task build              # build all packages (nexus, sdk, nexus-ui)
task build:workspace-daemon   # build Go daemon only
task build:workspace-sdk     # build TypeScript SDK only
```

### Daemon Development

```bash
task daemon:build          # compile the Go daemon
task daemon:restart       # kill existing daemon, start fresh, wait for /healthz
task daemon:logs           # tail ~/.config/nexus/run/daemon.log
```

### macOS App Development

```bash
task swift:prepare-resources  # embed locally built daemon
task swift:build             # build NexusApp.app via xcodebuild
task app:open                # launch NexusApp.app for eyeball testing
task dev                      # daemon:restart + app:open
```

### Swift UI Testing

```bash
task test:terminal    # run NexusTerminalUITests (requires daemon running + Accessibility)
task test:smoke      # quick smoke tests (launch, connect)
task test           # run all XCUITests
task test:unit       # run NexusAppTests (unit tests, no UI)
```

## Testing Tiers

Nexus has three testing tiers:

| Tier | What | How to Run |
|---|---|---|
| Unit | Go package tests, TypeScript package tests | `task test:workspace-daemon`, `task test:workspace-sdk` |
| Integration | Go integration tests with driver harness | `task test:workspace-daemon` (includes integration) |
| E2E | Playwright flows against live daemon + runtime | `task ci:flows-e2e` |

For local E2E without a runtime installed:
```bash
NEXUS_E2E_STRICT_RUNTIME=0 task ci:flows-e2e
```

## Release Pipeline

```
opencode (implementation)
    │  conventional commits
    ▼
gh pr create
    │  CI checks (task ci)
    ▼
merge to main
    │  release triggered by tag
    ▼
GitHub Actions → build + sign → publish
```

Release signing keys: see `docs/guides/release-signing.md`.

## Code Standards

### File Size Limits

| Layer | Limit |
|---|---|
| Core/domain logic | ≤300 lines |
| Orchestration/application logic | ≤400 lines |
| Transport/adapters/tests | ≤500 lines |

### Dependency Direction

```
domain → orchestration → transport/storage
tests → may depend on any layer
```

### Commit Convention

[Conventional Commits](https://www.conventionalcommits.org):

- `feat(workspace-daemon): add compose port auto-forward`
- `fix(workspace-sdk): align spotlight response types`
- `docs: update workspace config reference`
- `refactor(workspacemgr): split manager.go into smaller packages`

### Opening a PR

1. Ensure `task build` passes.
2. Ensure `task test` passes.
3. Update relevant docs in `docs/guides/` or `docs/reference/` if behavior changed.
4. Use a focused PR title matching Conventional Commits.
```

## Taskfile.yml Reorganization

### Proposed Structure

Tasks grouped by **persona** with clear sections:

```
## daemon — Go daemon development
nexus:build         Build nexus CLI + nexus-daemon into bin/
nexus:dev           Build + restart daemon
daemon:build        (legacy alias)
daemon:restart      Kill existing, start fresh, wait for /healthz
daemon:logs          Tail daemon log

## swift — macOS app (NexusApp)
swift:bundle-daemon    Build Go daemon with version ldflags → app Resources
swift:bundle-tools    Download fallback tools (lima/mutagen — NOW DEPRECATED, firecracker-only)
swift:prepare-resources Embed daemon; download tools only when NEXUS_BUNDLE_TOOLS=1
swift:build           xcodebuild NexusApp.app
swift:regen           Regenerate .xcodeproj from project.yml

## app — NexusApp usage
app:open           Launch NexusApp.app
app:kill           Quit running NexusApp
app:logs           Stream NexusApp + daemon logs
app:diag           Connectivity diagnostics
app:codesign       Ad-hoc code sign after manual binary swap

## dev — combined flows
dev               daemon:restart + app:open

## test — testing tiers
test:terminal     Run NexusTerminalUITests
test:smoke        Run quick smoke tests
test:unit         Run NexusAppTests (unit only)
test              Run all XCUITests
test:workspace-daemon  Go test ./...
test:workspace-sdk    pnpm test

## build — all packages
build                 Build all (nexus, sdk, nexus-ui)
build:workspace-daemon
build:workspace-sdk
build:nexus-ui

## lint — type/lint checks
lint                  Run all lint checks
lint:workspace-daemon
lint:workspace-sdk

## ci — CI pipeline equivalents
ci                    Full local CI (go-fix, coverage, core, flows-e2e)
ci:core               Core CI (Linux)
ci:go-fix             Go Fix Check
ci:coverage           Go Coverage
ci:flows-e2e          Runtime backend selection E2E

## housekeeping
clean               Remove all build artifacts
```

### Key Changes

1. **Add `## daemon` section** — group all daemon-related tasks clearly.
2. **Deprecate Lima/mutagen references** — `swift:bundle-tools` downloads Lima tooling that is no longer used in the runtime; add a comment noting this is for backwards-compatibility only.
3. **Make `nexus:build` explicit** — it produces `bin/nexus` and `bin/nexus-daemon` co-located so the CLI auto-discovers the daemon.
4. **Add `nexus:dev`** — one command to build + restart daemon.
5. **Add `## dev` section** — clear combined flows for common workflows.
6. **Add `## build` section** — all package builds in one place, matching `CONTRIBUTING.md`.
7. **Add `## lint` section** — type and lint checks parallel to test tiers.
8. **Clarify `ci:flows-e2e`** — document `NEXUS_E2E_STRICT_RUNTIME=0` for local runs without runtime.

## Testing Structure

### Current State

Go tests:
```
packages/nexus/pkg/**/*_test.go     # unit + integration tests
packages/nexus/test/integration/     # integration harness + tests
packages/nexus/test/integration/driver_test.go
packages/nexus/test/integration/harness.go
```

TypeScript tests:
```
packages/sdk/js/src/**/*.test.ts
packages/e2e/flows/src/**/*.test.ts
```

XCUITests:
```
NexusUITests/
  NexusTerminalUITests
  NexusUITests/
    testAppLaunches
    testConnectsOrShowsStartup
    testConnectionStatusIndicatorAppears
```

### Problems

1. `packages/nexus/pkg/workspacemgr/manager.go` is ~1269 lines, violating the ≤400 line orchestration limit. It should be split into focused packages.
2. `packages/nexus/pkg/handlers/workspace_manager.go` is ~1247 lines, severely violating limits.
3. `test/integration/` harness is underdocumented — unclear how to run integration tests.
4. No clear E2E documentation for external contributors.

### Proposed Testing Documentation

Add a `docs/guides/testing.md` guide covering:

- Unit test conventions
- How to run integration tests (daemon must be running; harness setup)
- E2E test structure and environment variables
- How to add new test cases

## Agent Skills Integration

### Current Skills

Groundwork workflow skills are loaded from:
- `~/.config/opencode/plugins/groundwork/skills/` (groundwork suite)
- `~/.opencode/skills/` (project-specific)

### Skill Triggers and Contributor Workflow

| Skill | Trigger | How It Helps |
|---|---|---|
| `create-prd` | Starting a non-trivial feature | Forces spec before code |
| `advisor-gate` | Any technical decision; always at task completion | Advisor fills in gaps |
| `nested-prd` | Architectural pivot mid-implementation | Documents scope change |
| `bdd-implement` | Any visible UI change or bug | Validates with actual visual inspection |
| `commit` | Creating git commits | Ensures conventional commit style |
| `nexus-macos-dev` | Building/ running NexusApp | Step-by-step macOS dev workflow |
| `bumping-app-version` | Version bumps | Standardized version increment |

### Proposed

1. Add `nexus-macos-dev` and `bumping-app-version` skill paths to `AGENTS.md` so agents know where to find them.
2. Create a `docs/guides/agent-skills.md` guide explaining how contributors can use the groundwork workflow skills.
3. Add a `docs/guides/testing.md` guide (see Testing Structure above).

## Release Pipeline

### Current Flow

1. Contributor opens PR → CI runs `task ci` (go-fix, coverage, core, flows-e2e)
2. PR merged → tag created → GitHub Actions builds + signs + publishes

### Proposed Release Documentation

Add `docs/guides/release-pipeline.md` covering:

1. Version bumping (`task bumping-app-version` or manual)
2. Tag format (`vMAJOR.MINOR.PATCH`)
3. CI jobs that run on tag
4. Release signing requirements

## Known Limitations

- **manager.go and workspace_manager.go severely exceed file size limits** — priority refactor targets. The workspacemgr package (~1269 lines) should be split into `workspacemgr/workspace.go` (core workspace entity) + `workspacemgr/manager.go` (orchestration, ~400 lines) + focused sub-packages (e.g., `workspacemgr/checkout.go`, `workspacemgr/fork.go`).
- **Lima/mutagen tooling in `swift:bundle-tools`** is downloaded but no longer used — clean up or document as deprecated.
- **E2E tests require a running daemon** — harness errors when `NEXUS_DAEMON_WS` / `NEXUS_DAEMON_TOKEN` are unset. Document this clearly.
- **`docs/roadmap.md` is absent** — needs creation.
- **Agent skills are not documented for contributors** — `docs/guides/agent-skills.md` needs creation.

## Steer Log

### 2026-04-17 — PRD created

- **Trigger**: User requested overhaul of docs, Taskfile, testing, and agent skills for external contributor onboarding and release pipeline.
- **From**: Scattered docs, no contributor guide, Lima references throughout.
- **To**: Unified contributor guide, refreshed Taskfile, testing documentation, agent skills integration.
- **Rationale**: Nexus completed firecracker-only simplification; docs must reflect current architecture accurately.
- **Affected sections**: All sections above.

### 2026-04-17 — Advisor corrections (multiple passes completed)

**Critical corrections before implementation:**

- **CONTRIBUTING.md already exists** (81 lines) — the PRD proposed a new `contributor-guide.md` that would duplicate it. Decision: update `CONTRIBUTING.md` instead. The existing file covers setup, build, test, E2E, architecture, commits, and PRs.

- **Agent Skills spec was researched** — the PRD's agent skills section was based on wrong assumptions about the skill file format. After reading agentskills.io/specification, the correct format uses YAML frontmatter (`name`, `description` required) + Markdown body, with optional `scripts/`, `references/`, `assets/` subdirs. Skills should be under 500 lines.

- **Lima removal incomplete in codebase** — the PRD correctly identified Lima as removed from runtime, but did not audit all references. Lima string references found in: `packages/nexus/pkg/handlers/workspace_manager.go:1180` (error message), `packages/nexus/pkg/runtime/factory_test.go:53` (test), `packages/nexus-swift/Sources/NexusCore/ConfigSyncManager.swift:164,179-182` (Swift code), `.github/workflows/ci.yml` (CI), `packages/nexus/test/integration/harness.go:23` (harness comment). These need targeted removal, not just docs updates.

- **`docs/superpowers/plans/` is empty** — no content found at that path. The `superpowers` directory in docs may be unused.

**Implementation decisions made:**

- **No new `contributor-guide.md`** — update `CONTRIBUTING.md` instead. New docs created: `roadmap.md`, `testing.md`, `agent-skills.md`.
- **`swift:bundle-tools` marked DEPRECATED** in Taskfile with inline comments explaining Lima is no longer used.
- **`workspace-config.md:37` fixed** — removed "Firecracker via Lima when available, otherwise seatbelt fallback" replaced with "Firecracker when nested virtualization is available, process fallback otherwise".
- **Agent Skills guide created** at `docs/guides/agent-skills.md` based on actual agentskills.io spec, not guesswork.
- **Groundwork workflow skill triggers** are now documented in `CONTRIBUTING.md` and `AGENTS.md` for contributor visibility.

**File size corrections:**

- `workspacemgr/manager.go` confirmed at **1268 lines** (severely over 300/400 limit — priority refactor target)
- `handlers/workspace_manager.go` confirmed at **1246 lines** (severely over 400 limit — priority refactor target)

**Known remaining work (out of scope for this PRD, tracked in roadmap):**

- Remove Lima/process-sandbox driver references from Swift `ConfigSyncManager.swift`, CI workflows, and harness comments
- Decompose `manager.go` and `workspace_manager.go` into focused sub-packages
