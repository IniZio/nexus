# Known flaky tests

Tests listed here have been observed to fail non-deterministically while
the surrounding suite passes. This file is git-tracked so provenance is
auditable; MAP.md is auto-generated and must not be used as the record.

---

## `internal/core/builder`

### `TestCacheDisk_DirtyLease_WipesOnNextReuse`

**Observed:** Failed for one agent run; passed in the advisor's subsequent
full `-count=1` run (`internal/core/builder` ok, 5.597s) with all skip
preconditions satisfied (`mke2fs`, `debugfs` present, no `/dev/vda`), so
the test genuinely executed rather than being skipped.

**Caveats — both apply:**

1. The "documented pre-existing flake" note previously lived only in
   `MAP.md`, which is untracked by git. Its provenance is a prior agent's
   claim, not independent evidence.

2. The characterisation of this flake as "unrelated" to the current motive
   is **unproven**. The process-global pin registry the test depends on was
   introduced by motive `nexus3-host-supervisor-hotswap` in commit
   `dae7ebf`. Related-but-intermittent and unrelated-and-intermittent are
   different findings, and only the former has evidence. Do not record this
   as definitively unrelated until the race is isolated.

**Skip preconditions:** skipped when `mke2fs`/`debugfs` absent or `/dev/vda`
present (runs inside a VM).

---

### `TestExportAndUnpack_UnpackError_Propagates`

**Observed:** Racing under the full parallel gate (`make test` with default
concurrency).

**Caveats:** Observed racing but root cause not yet isolated — may be a
test-ordering or shared-state interaction with other tests in the same
package.

---
