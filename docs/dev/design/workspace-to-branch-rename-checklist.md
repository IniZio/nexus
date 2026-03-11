# Workspace-to-Branch Rename Checklist (Future Migration)

Use this checklist when code migration from workspace-first terms to branch/dev-session terms begins.

This checklist is future-only planning. It does not imply the migration is already implemented.

## 1) CLI and Command Surfaces

- [ ] Review `packages/nexusd/internal/cli/workspace.go` for command nouns, help text, flags, and error messages.
- [ ] Update `packages/nexusd/internal/cli/root.go` command registration and future group wiring order.
- [ ] Update `packages/nexusd/internal/cli/cli_test.go` for renamed command grammar and help expectations.

## 2) Server API and Runtime Semantics

- [ ] Audit `packages/nexusd/pkg/server/server.go` request/response fields and route semantics that currently use workspace naming.
- [ ] Define staged compatibility for API payload keys if rollout requires mixed clients.
- [ ] Confirm one-active-session-per-branch behavior is enforced in server lifecycle transitions.

## 3) Persisted State and Naming Compatibility

- [ ] Review `packages/nexusd/internal/state/store.go` path/file conventions and serialized object keys.
- [ ] Plan compatibility read/write behavior for existing state data created under workspace names.
- [ ] Define migration sequencing for persisted IDs and user-visible labels.

## 4) Worktree and Branch Coupling

- [ ] Review `packages/nexusd/internal/git/worktree.go` branch/worktree assumptions against branch-owned dev-session semantics.
- [ ] Validate fork-first branch workflows as the parallelism model.

## 5) Test Surface Impact

- [ ] Update E2E assertions in `packages/nexusd/test/e2e/hanlun_test.go`.
- [ ] Update integration assumptions in `packages/nexusd/test/integration/workspace_test.go`.
- [ ] Update command-level assertions in `packages/nexusd/internal/cli/cli_test.go`.

## 6) Telemetry and Config Compatibility

- [ ] Audit telemetry event names and dimensions for workspace terminology and define staged migration.
- [ ] Audit config keys and defaults in `packages/nexusd/internal/config/config.go`.
- [ ] Decide whether telemetry/config need temporary dual naming during rollout.

## 7) Docs and Contract Boundaries

- [ ] Keep user-facing docs truthful to implemented commands until project-first commands are actually shipped.
- [ ] Keep Box references internal-only.
- [ ] Do not promise compatibility aliases in user-facing docs unless implementation exists.
