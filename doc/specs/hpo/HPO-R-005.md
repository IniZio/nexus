---
id: HPO-R-005
type: requirement
concept: C-HPO
criticality: must
verification: automated
status: active
origin_decision_ref: herdr-plugin-ootb#D-4
summary: "install-default-shell --write-config writes config.toml idempotently across all four config states."
---

## HPO-R-005 — config.toml wiring is automatic and idempotent {#hpo-r-005}

**When** `nexus3 install-default-shell --write-config` is invoked, the system **shall** write the `[terminal] default_shell` entry to `~/.config/herdr/config.toml` across all four config states (file absent, section absent, key absent, key already present with the same value) without ever leaving `herdr config check` in a failing state, and **shall** be a no-op when the entry already holds the correct value.

- **Why** — if any of the four config states is mishandled the user is left with a broken `config.toml` that fails `herdr config check`, causing every subsequent herdr invocation to warn or refuse; the idempotency invariant ensures a re-run of install or an upgrade cannot corrupt a working config.
- **Fit criterion** — a test exercises all four states in sequence and asserts: (a) `herdr config check` exits 0 after each invocation; (b) the entry appears exactly once in the file; (c) a second invocation on an already-correct file changes no bytes. A fifth state exercises chain-and-rewrite when `--next` is supplied.
- **Verification**: automated — `TestInstallDefaultShellWriteConfig` in the `herdr_live` suite covers all four states plus the `--next` chain path.
- **Criticality**: must
- **See also** [HPO-R-001](#hpo-r-001)
