# Spec Index

_Generated: 2026-09-16T07:35:55.762Z_

## Concepts

| Concept | Summary | Status | Views |
| --- | --- | --- | --- |
| C-CRED | Requirements for live-mount credential delivery to claude-code sandboxes, auto permission mode, git SSH relay with policy guard, and port auto-forward. | — | — |
| C-HPO | Requirements for zero-manual-step install and upgrade of the nexus3 herdr plugin on Linux and macOS, with a real-herdr test harness and focused port-forwards. | — | — |
| C-PDF | Requirements for the N-sandbox parallel development flow ending in GitHub PRs and a downloadable built-output preview artifact. | — | — |
| C-PER | Requirements for the per-sandbox detached supervisor that keeps the egress perimeter and credential broker alive after the spawning CLI exits. | — | — |
| C-RES | Requirements for deterministic resource ownership, leak-free reclamation, journaled creation, and substrate-first crash recovery. | — | — |
| C-SUR | Requirements for a canonical API surface, uniform MCP envelope, ephemeral one-call exec, surface-parity enforcement, and MCP verb parity. | — | — |

## Herdr plugin out-of-the-box

### [HPO-R-001 — Linux x86-64 one-command fresh install](../hpo/HPO-R-001.md#hpo-r-001)

### [HPO-R-002 — macOS remote-client one-command fresh install](../hpo/HPO-R-002.md#hpo-r-002)

### [HPO-R-003 — Idempotent upgrade; dev and newer builds preserved](../hpo/HPO-R-003.md#hpo-r-003)

### [HPO-R-004 — ABI/version skew reported with the exact update command](../hpo/HPO-R-004.md#hpo-r-004)

### [HPO-R-005 — config.toml wiring is automatic and idempotent](../hpo/HPO-R-005.md#hpo-r-005)

### [HPO-R-006 — Install-time preflight reports substrate problems with remediation](../hpo/HPO-R-006.md#hpo-r-006)

### [HPO-R-007 — Install/update/startup path exercised against a real herdr in an isolated session](../hpo/HPO-R-007.md#hpo-r-007)

### [HPO-R-008 — Remote-client port-forwards scoped to the focused workspace](../hpo/HPO-R-008.md#hpo-r-008)

### [HPO-R-009 — Locally bound port reported as conflict, never marked forwarded](../hpo/HPO-R-009.md#hpo-r-009)

### [HPO-R-010 — Forwards cancelled on client restart; duplicate rows collapsed](../hpo/HPO-R-010.md#hpo-r-010)
