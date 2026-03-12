# Product Interface Hard Cutover Design

## Context

Nexus currently exposes a workspace-first and trace-first CLI surface. The target product model is:

- Org
- Project
- Branch
- Version
- Environment

The goal is a hard cutover so user-facing docs and user-facing CLI align to the target model now, with implementation following quickly behind.

## Approved Design

### 1) Cutover Shape

- Treat `Org -> Project -> Branch -> Version -> Environment` as canonical immediately.
- Shift docs, CLI surface, and internal terms toward that model in the same migration stream.
- Stop presenting `workspace` and `trace` as user-facing primary nouns.
- Keep any temporary compatibility behavior implementation-side only; do not anchor user-facing docs on legacy terms.

### 2) Command Interface Contract

New user-facing top-level nouns:

- `nexus project`
- `nexus branch`
- `nexus version`
- `nexus environment`

Command semantics:

- `project`: project scope and project-level operations.
- `branch`: development line operations.
- `version`: immutable build or release artifacts.
- `environment`: runtime targets and execution contexts.

Transition constraints:

- Current binary version output must move away from `nexus version` to avoid semantic conflict (for example `nexus cli-version`).
- Existing workspace behavior maps to environment behavior.
- Existing trace behavior maps to lifecycle observability associated with version and environment workflows.

### 3) Execution and Safety Plan

Perform in sequential batches with verification after each:

1. Surface contract batch
   - Introduce target command tree and help text.
   - Add compatibility routing where needed for short-lived transition.
2. Behavior migration batch
   - Rebind current workspace and trace logic under target nouns.
3. Documentation and examples batch
   - Rewrite all user-facing docs and examples to target interface only.
4. Legacy removal batch
   - Remove legacy user-facing command paths and stale terms.

Verification gates per batch:

- `go run ./cmd/cli --help` reflects the target top-level model.
- Target command groups show accurate `--help` output.
- Existing and updated tests pass.
- User-facing docs contain no stale primary nouns for deprecated interface terms.

Stop conditions:

- Command namespace collisions (especially `version`) block progress until resolved.
- Failing interface tests block the next batch.

## Notes

- This design intentionally prioritizes target product clarity over preserving current user-facing nomenclature.
- Operational compatibility can exist temporarily in code paths, but user-facing docs should represent the target interface.
