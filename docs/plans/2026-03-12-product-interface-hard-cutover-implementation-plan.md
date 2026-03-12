# Product Interface Hard Cutover Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace the user-facing Nexus interface with the target model (`project`, `branch`, `version`, `environment`) across CLI, docs, and examples.

**Architecture:** Introduce new top-level Cobra commands first, then rebind existing workspace and trace behavior under target nouns, then remove legacy user-facing command paths. Guard the cutover with interface tests and help-output verification at each stage.

**Tech Stack:** Go (Cobra CLI in `packages/nexusd`), Markdown docs in `docs/`, shell verification via `go run` and `go test`.

---

### Task 1: Lock the New Top-Level Command Contract in Tests

**Files:**
- Create: `packages/nexusd/internal/cli/interface_surface_test.go`
- Modify: `packages/nexusd/internal/cli/root.go`

**Step 1: Write the failing test**

Add tests that assert root command names include `project`, `branch`, `version`, and `environment`, and do not include `workspace` or `trace`.

**Step 2: Run test to verify it fails**

Run: `go test ./internal/cli -run TestRootCommandSurface -v`
Expected: FAIL because current root command still registers `workspace` and `trace`.

**Step 3: Write minimal implementation**

Register placeholder commands in `root.go` for:
- `project`
- `branch`
- `environment`

Keep `version` present, but update help text on root to target nouns.

**Step 4: Run test to verify it passes**

Run: `go test ./internal/cli -run TestRootCommandSurface -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add packages/nexusd/internal/cli/interface_surface_test.go packages/nexusd/internal/cli/root.go
git commit -m "test: lock target root command surface"
```

### Task 2: Resolve Version Command Collision

**Files:**
- Modify: `packages/nexusd/internal/cli/root.go`
- Create: `packages/nexusd/internal/cli/cli_version.go`
- Test: `packages/nexusd/internal/cli/interface_surface_test.go`

**Step 1: Write the failing test**

Add tests that require:
- `nexus version` is reserved for product version workflows (placeholder acceptable initially).
- Binary version output is available at `nexus cli-version`.

**Step 2: Run test to verify it fails**

Run: `go test ./internal/cli -run TestVersionCommandSplit -v`
Expected: FAIL because binary version is currently bound to `version`.

**Step 3: Write minimal implementation**

- Move existing binary version printing behavior into `cli-version` command.
- Keep `version` command as target-interface entrypoint (placeholder subcommands allowed until Task 5).

**Step 4: Run test to verify it passes**

Run: `go test ./internal/cli -run TestVersionCommandSplit -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add packages/nexusd/internal/cli/root.go packages/nexusd/internal/cli/cli_version.go packages/nexusd/internal/cli/interface_surface_test.go
git commit -m "feat: split product version and cli-version commands"
```

### Task 3: Introduce Environment Command and Rebind Workspace Entry

**Files:**
- Create: `packages/nexusd/internal/cli/environment.go`
- Modify: `packages/nexusd/internal/cli/workspace.go`
- Modify: `packages/nexusd/internal/cli/root.go`
- Test: `packages/nexusd/internal/cli/interface_surface_test.go`

**Step 1: Write the failing test**

Add tests asserting `environment` exposes subcommands equivalent to existing workspace operations (`create`, `delete`, `list`, `status`, `start`, `stop`, `exec`, `ssh`, `use`, `logs`, `checkpoint`, `inject-key`).

**Step 2: Run test to verify it fails**

Run: `go test ./internal/cli -run TestEnvironmentCommandSurface -v`
Expected: FAIL because `environment` does not yet expose subcommands.

**Step 3: Write minimal implementation**

- Create `environmentCmd` and attach workspace behavior handlers.
- Keep shared implementations DRY by reusing current command constructors instead of duplicating command logic.

**Step 4: Run test to verify it passes**

Run: `go test ./internal/cli -run TestEnvironmentCommandSurface -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add packages/nexusd/internal/cli/environment.go packages/nexusd/internal/cli/workspace.go packages/nexusd/internal/cli/root.go packages/nexusd/internal/cli/interface_surface_test.go
git commit -m "feat: add environment command backed by workspace behavior"
```

### Task 4: Introduce Project and Branch Command Scaffolds with Real Validation

**Files:**
- Create: `packages/nexusd/internal/cli/project.go`
- Create: `packages/nexusd/internal/cli/branch.go`
- Modify: `packages/nexusd/internal/cli/root.go`
- Test: `packages/nexusd/internal/cli/interface_surface_test.go`

**Step 1: Write the failing test**

Add tests for:
- `project` and `branch` appear in root help.
- each has at least one actionable subcommand (`list` for project, `use` for branch) with proper arg validation.

**Step 2: Run test to verify it fails**

Run: `go test ./internal/cli -run TestProjectAndBranchScaffolds -v`
Expected: FAIL until subcommands and validation are implemented.

**Step 3: Write minimal implementation**

- Add minimal but real command handlers (non-placeholder output + validation errors on missing args).
- Keep behavior YAGNI: do not add persistence yet beyond existing config/session mechanisms.

**Step 4: Run test to verify it passes**

Run: `go test ./internal/cli -run TestProjectAndBranchScaffolds -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add packages/nexusd/internal/cli/project.go packages/nexusd/internal/cli/branch.go packages/nexusd/internal/cli/root.go packages/nexusd/internal/cli/interface_surface_test.go
git commit -m "feat: add project and branch command scaffolds"
```

### Task 5: Re-home Trace Workflows Under Version and Environment

**Files:**
- Modify: `packages/nexusd/internal/cli/trace.go`
- Modify: `packages/nexusd/internal/cli/environment.go`
- Modify: `packages/nexusd/internal/cli/root.go`
- Test: `packages/nexusd/internal/cli/interface_surface_test.go`

**Step 1: Write the failing test**

Add tests requiring trace functionality to be reachable from target nouns (for example `nexus version history ...` and/or `nexus environment activity ...`), and ensure top-level `trace` is no longer user-facing.

**Step 2: Run test to verify it fails**

Run: `go test ./internal/cli -run TestTraceRehomedUnderTargetModel -v`
Expected: FAIL with current top-level `trace`.

**Step 3: Write minimal implementation**

- Introduce target-model command group(s) that delegate to existing trace handlers.
- Remove top-level `trace` from root command registration.

**Step 4: Run test to verify it passes**

Run: `go test ./internal/cli -run TestTraceRehomedUnderTargetModel -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add packages/nexusd/internal/cli/trace.go packages/nexusd/internal/cli/environment.go packages/nexusd/internal/cli/root.go packages/nexusd/internal/cli/interface_surface_test.go
git commit -m "feat: rehome trace workflows under target interface"
```

### Task 6: Remove Legacy User-Facing Workspace and Trace Commands

**Files:**
- Modify: `packages/nexusd/internal/cli/root.go`
- Modify: `packages/nexusd/internal/cli/workspace.go`
- Test: `packages/nexusd/internal/cli/interface_surface_test.go`

**Step 1: Write the failing test**

Add explicit regression tests to fail if `workspace` or `trace` is registered as a root command.

**Step 2: Run test to verify it fails**

Run: `go test ./internal/cli -run TestLegacyRootCommandsRemoved -v`
Expected: FAIL until legacy registrations are fully removed.

**Step 3: Write minimal implementation**

- Remove root command registrations for `workspace` and `trace`.
- Keep only internal helpers needed by new command groups.

**Step 4: Run test to verify it passes**

Run: `go test ./internal/cli -run TestLegacyRootCommandsRemoved -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add packages/nexusd/internal/cli/root.go packages/nexusd/internal/cli/workspace.go packages/nexusd/internal/cli/interface_surface_test.go
git commit -m "refactor: remove legacy workspace and trace root commands"
```

### Task 7: Rewrite User-Facing Documentation to Target Interface

**Files:**
- Modify: `docs/index.md`
- Modify: `docs/tutorials/workspace-quickstart.md` (rename to `docs/tutorials/environment-quickstart.md` if desired)
- Modify: `docs/reference/nexus-cli.md`
- Modify: `docs/examples/README.md`
- Modify: `docs/examples/quickstart/README.md`
- Modify: `docs/examples/remote-server/README.md`
- Modify: `docs/examples/node-react/README.md`
- Modify: `docs/examples/python-django/README.md`
- Modify: `docs/examples/go-microservices/README.md`
- Modify: `docs/examples/fullstack-postgres/README.md`

**Step 1: Write the failing doc guard checks**

Define guard commands that fail on legacy nouns/commands in user-facing docs.

**Step 2: Run guard checks to verify they fail first**

Run:

```bash
rg -n --glob '*.md' 'nexus workspace|nexus trace|workspace-first|Workspace Quickstart' docs/index.md docs/tutorials docs/reference docs/explanation docs/examples
```

Expected: FAIL (matches present before rewrite).

**Step 3: Rewrite docs with target nouns and target command examples**

- Replace workspace-first framing with environment-first framing.
- Ensure command examples match the new implemented command tree.
- Keep docs truthful to post-cutover CLI behavior only.

**Step 4: Run guard checks to verify they pass**

Run:

```bash
rg -n --glob '*.md' 'nexus workspace|nexus trace|workspace-first|Workspace Quickstart' docs/index.md docs/tutorials docs/reference docs/explanation docs/examples
```

Expected: no matches.

**Step 5: Commit**

```bash
git add docs/index.md docs/tutorials docs/reference/nexus-cli.md docs/examples
git commit -m "docs: cut over user docs to project-branch-version-environment model"
```

### Task 8: End-to-End Interface Verification and Cleanup

**Files:**
- Modify: `packages/nexusd/internal/cli/cli_test.go`
- Modify: `packages/nexusd/internal/cli/interface_surface_test.go`
- Modify: `docs/plans/2026-03-11-product-interface-truth-matrix.md`

**Step 1: Write failing integration assertions**

Add tests that parse root and subgroup help output and assert only target interface nouns are present.

**Step 2: Run tests to verify failure before final cleanup**

Run: `go test ./internal/cli -run TestHelpOutputTargetInterface -v`
Expected: FAIL until all stale strings are removed.

**Step 3: Implement final cleanup**

- Remove stale wording from help strings.
- Update truth matrix status rows to reflect implemented cutover.

**Step 4: Run full verification suite**

Run:

```bash
go test ./internal/cli -v
go test ./... 
go run ./cmd/cli --help
go run ./cmd/cli project --help
go run ./cmd/cli branch --help
go run ./cmd/cli version --help
go run ./cmd/cli environment --help
```

Expected: tests pass; help output shows target interface and no legacy root nouns.

**Step 5: Commit**

```bash
git add packages/nexusd/internal/cli docs/plans/2026-03-11-product-interface-truth-matrix.md
git commit -m "test: enforce target interface help and docs consistency"
```

## Final Validation Checklist

- `nexus --help` exposes target nouns (`project`, `branch`, `version`, `environment`) as primary interface.
- Binary version command is no longer overloaded on `nexus version`.
- No user-facing docs under `docs/` advertise `workspace` or `trace` as primary commands.
- `go test ./...` passes from `packages/nexusd`.
