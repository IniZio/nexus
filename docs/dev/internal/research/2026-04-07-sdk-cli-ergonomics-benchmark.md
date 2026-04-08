# SDK/CLI Ergonomics Benchmark (Nexus)

Date: 2026-04-07  
Status: Draft reference for SDK/CLI design decisions

## Why this document exists

Nexus now serves two high-frequency consumers with different usage profiles:

- `opencode` Nexus plugin (automation-heavy, long-running, multi-workspace sessions)
- Embedded management UI (interactive, task-driven, action-oriented)

We need one coherent SDK/CLI design model that is:

- semantically clear (nouns and verbs match actual contracts)
- ergonomic for default workflows
- explicit for cross-workspace and control-plane operations

This document benchmarks relevant ecosystems and extracts concrete design rules for Nexus.

## Scope and non-goals

In scope:

- SDK object model and method shape
- CLI command taxonomy and context semantics
- plugin/UI usage ergonomics

Out of scope:

- transport-level wire protocol changes
- runtime backend policy changes
- compatibility with removed legacy doc paths

## External benchmarks

### 1) Daytona (workspace/sandbox platform)

Observed signals:

- Top-level client constructor (`new Daytona(...)`) with config/env-default behavior.
- Resource-oriented surface under one client (`sandbox`, `snapshot`, `volume`, etc.).
- CLI command tree uses noun + lifecycle verbs consistently (`create`, `list`, `info`, `start`, `stop`, `delete`, `exec`).
- CLI supports discoverability via `--help` everywhere and structured output flags (`--format json|yaml`).

Takeaway for Nexus:

- Keep one top-level client and group capabilities by domain.
- Preserve predictable verb vocabulary across SDK and CLI.
- Treat listing/inspection vs lifecycle mutation as distinct command families.

### 2) Octokit (API client design)

Observed signals:

- Two complementary access modes:
  - typed convenience endpoint methods (`octokit.rest.*`)
  - generic request escape hatch (`octokit.request(...)`)
- Strong constructor defaults plus per-request override options.
- Plugin/extensibility model without breaking core API shape.

Takeaway for Nexus:

- Keep ergonomic typed methods as primary path.
- Provide explicit low-level escape hatch only if necessary, not as first-class default.
- Favor stable core abstractions over ad hoc helper sprawl.

### 3) Supabase JS client

Observed signals:

- `createClient(...)` establishes ambient project context once.
- Subsequent operations are scoped by default to that context.
- Sub-clients expose domain-specific operations with consistent chaining/shape.

Takeaway for Nexus:

- Default scoping to configured workspace context is ergonomic and expected.
- Explicit override should remain possible for admin/control-plane workflows.

### 4) kubectl context model

Observed signals:

- Strong default context (current cluster/namespace).
- Explicit context/namespace overrides for cross-context actions.
- Commands remain composable without requiring repeated identifiers.

Takeaway for Nexus:

- Workspace context should be first-class.
- Cross-workspace operations should require explicit override to avoid accidental targeting.

### 5) AWS SDK v3 (service clients)

Observed signals:

- Explicit client instances per service/context.
- Structured command surface and clear environment portability story.
- Predictable constructor configuration + composable middleware/extension patterns.

Takeaway for Nexus:

- Keep constructor-driven configuration authoritative.
- Avoid hidden global mutable state; scope should come from client/handle instances.

### 6) Forge ecosystem note

The query term "forgevm" was ambiguous in available sources. Retrieved material was primarily Atlassian Forge CLI and unrelated "Forge" projects.

Usable signal from Forge CLI:

- CLI-first workflow with explicit deploy/install/tunnel/logs lifecycle commands.
- clear authentication/account commands (`login`, `logout`, `whoami`)

Takeaway for Nexus:

- Keep operator/auth/session commands explicit in CLI and separate from workspace lifecycle verbs.

## Nexus design principles (proposed)

### P1. Context-first ergonomics

- `WorkspaceClient` defines default workspace context.
- `client.fs`, `client.exec`, `client.spotlight` should use default context unless explicitly overridden.
- `WorkspaceHandle` remains always explicitly scoped and preferred for multi-workspace flows.

Why: reduces boilerplate in plugin and UI while preserving safety for cross-workspace actions.

### P2. Noun/verb semantic alignment

- SDK and CLI should share a stable lifecycle verb set: `create`, `list`, `open/get`, `start`, `pause`, `resume`, `stop`, `restore`, `remove`, `fork`.
- Use one canonical verb per semantic action (avoid synonyms unless protocol-constrained).

Why: predictability improves onboarding and reduces misuse in automation.

### P3. Dual-path control surface

- Primary path: typed domain APIs (high ergonomics).
- Secondary path: controlled lower-level request path for advanced use (if exposed, clearly marked advanced).

Why: avoids overfitting core API while keeping power-user escape hatches.

### P4. Explicit cross-workspace override

- Any operation that can act outside default context must accept explicit workspace id override.
- Overrides should be visually obvious in code (`client.spotlight.list('ws-...')`).

Why: multi-workspace safety and debuggability.

### P5. Plugin/UI parity

- Every UI-backed action should map to one SDK method and one daemon endpoint semantics.
- Avoid UI-only semantic branches not represented in SDK.

Why: shared mental model across automation and interactive control planes.

## Nexus implications (current and near-term)

Already aligned:

- `client.fs`, `client.exec`, and `client.spotlight` default workspace scoping.
- explicit spotlight workspace override supported.
- `WorkspaceHandle` scoped operation model.
- unified docs naming (`sdk.md`, `cli.md`) to reflect broader surfaces.

Gaps / follow-ups:

1. Type hygiene in SDK types

- `packages/sdk/js/src/types.ts` contains duplicated interface declarations (pre-existing debt).
- Action: normalize to single authoritative definitions.

2. Documented capability matrix

- Add concise matrix in `docs/reference/sdk.md` mapping:
  - default-scoped methods
  - explicitly-overridable methods
  - handle-scoped methods

3. CLI taxonomy review

- Align CLI sections by intent buckets:
  - session/auth
  - workspace lifecycle
  - execution/files
  - forwarding/network
  - diagnostics

4. UI/API naming consistency check

- Ensure embedded UI action labels and endpoints match SDK verbs exactly.

## Recommended canonical model (v1)

### SDK

- Context provider: `new WorkspaceClient({ endpoint, workspaceId, token })`
- Default-scoped operations:
  - `client.fs.*`
  - `client.exec.exec(...)`
  - `client.spotlight.*` (override optional where supported)
- Lifecycle/orchestration:
  - `client.workspace.*`
- Explicit scoped handle:
  - `const ws = await client.workspace.open(id)` then `ws.fs/ws.exec/ws.spotlight/ws.git/ws.service`

### CLI

- Operator auth/session commands separate from workspace lifecycle.
- Lifecycle commands use canonical verbs only.
- Machine-readable output mode is available for list/info/status commands.

## Decision log hooks

Candidate ADRs from this research:

- "Default workspace context with explicit cross-workspace override"
- "Canonical lifecycle verb set across SDK/CLI/UI"
- "Scoped handle as preferred multi-workspace programming model"

## Sources consulted

- Daytona docs (CLI + TypeScript SDK)
- Daytona SDK repository and monorepo references
- Octokit docs/repo README and endpoint-method model
- Supabase JS client references and examples
- Kubernetes kubectl context/config references
- AWS SDK for JavaScript v3 developer guide
- Atlassian Forge CLI reference (as nearest available Forge signal)
