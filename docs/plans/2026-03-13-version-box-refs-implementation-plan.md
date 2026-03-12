# Version-Box Refs Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Implement the corrected product model where Version owns Box, and Branch/Environment are refs to versions, with clear writable/snapshot/restore semantics.

**Architecture:** Keep current workspace runtime as box substrate, add minimal version/ref metadata and lifecycle rules first, then expose user-facing CLI only when behavior is real.

**Tech Stack:** Go (Cobra CLI + daemon), existing state/proto/storage, Markdown docs.

---

### Task 1: Add version/ref domain model (internal only)

**Files:**
- Create: `packages/nexusd/internal/types/version.go`
- Create: `packages/nexusd/internal/types/refs.go`
- Modify: `packages/nexusd/internal/types/types.go`
- Test: `packages/nexusd/internal/types/version_refs_test.go`

**Steps:**
1. Define `VersionState` (`scratch`, `draft`, `immutable`) and version metadata struct.
2. Define `BranchRef` / `EnvironmentRef` structures with `version_id`.
3. Add tests for state transitions and ref validation.
4. Run: `go test ./internal/types -v`

### Task 2: Add metadata persistence over existing box substrate

**Files:**
- Create: `packages/nexusd/internal/state/version_store.go`
- Create: `packages/nexusd/internal/state/ref_store.go`
- Test: `packages/nexusd/internal/state/version_ref_store_test.go`

**Steps:**
1. Persist version metadata (not box payload duplication).
2. Persist branch/environment refs to version IDs.
3. Add tests for create/read/update and missing-ID behavior.
4. Run: `go test ./internal/state -v`

### Task 3: Implement snapshot/restore lifecycle semantics

**Files:**
- Modify: `packages/nexusd/internal/lifecycle/lifecycle.go`
- Modify: `packages/nexusd/internal/checkpoint/*.go`
- Test: `packages/nexusd/internal/lifecycle/version_lifecycle_test.go`

**Steps:**
1. Map snapshot to new version creation from scratch box.
2. Map restore to writable scratch materialization + ref move.
3. Enforce immutable non-write policy in lifecycle paths.
4. Run: `go test ./internal/lifecycle -v`

### Task 4: Expose minimal truthful CLI for refs/versions

**Files:**
- Modify: `packages/nexusd/internal/cli/root.go`
- Create/Modify: `packages/nexusd/internal/cli/version*.go`
- Create/Modify: `packages/nexusd/internal/cli/branch*.go`
- Create/Modify: `packages/nexusd/internal/cli/environment*.go`
- Test: `packages/nexusd/internal/cli/interface_surface_test.go`

**Steps:**
1. Add only commands backed by real behavior.
2. Keep scaffold labels explicit where behavior is not yet shipped.
3. Remove user-facing trace surfaces in this scope.
4. Run: `go test ./internal/cli -v`
5. Verify help: `go run ./cmd/cli --help`

### Task 5: Rewrite user-facing docs to match corrected model

**Files:**
- Modify: `docs/index.md`
- Modify: `docs/tutorials/*.md`
- Modify: `docs/reference/*.md`
- Modify: `docs/explanation/*.md`

**Steps:**
1. Document Version->Box ownership and refs model.
2. Explain writable scratch vs immutable versions clearly.
3. Keep examples aligned with implemented CLI only.
4. Run doc scans for banned stale patterns.

### Task 6: End-to-end verification

**Files:**
- Modify: `docs/plans/2026-03-11-product-interface-truth-matrix.md`

**Steps:**
1. Run full suite: `go test ./...` from `packages/nexusd`.
2. Run CLI help checks for exposed commands.
3. Run user-doc truth scans over `docs/index.md docs/tutorials docs/reference docs/explanation docs/examples`.
4. Update truth matrix status and unresolved items.

## Notes

- This plan intentionally avoids early broad renaming of internal workspace substrate.
- User-facing terms should lead only after behavior exists.
