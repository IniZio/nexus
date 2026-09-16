---
id: HPO-R-007
type: requirement
concept: C-HPO
criticality: must
verification: automated
status: active
origin_decision_ref: herdr-plugin-ootb#D-2
summary: "Install/update/startup path runs against a real herdr in an isolated named session."
---

## HPO-R-007 — Install/update/startup path exercised against a real herdr in an isolated session {#hpo-r-007}

**When** the `herdr_live` test suite runs, every test that drives install, upgrade, or `[[startup]]` behavior **shall** operate against a real herdr 0.9.0 binary in a named session under a redirected config root, and **shall** refuse to execute when the resolved config root equals the operator's live `~/.config/herdr`, ensuring no test mutates the operator's plugin registry.

- **Why** — the existing `fakeHerdrExec` stub proves only argv shape; it cannot detect that herdr rejects a manifest field, fails the build hook, or refuses the plugin registration. A live herdr instance in an isolated session is the only way to prove the install contract end-to-end without KVM or VM boot.
- **Fit criterion** — `make test-herdr-live` passes with herdr 0.9.0 installed; every test in the suite sets `HERDR_CONFIG_PATH` to a temp dir and aborts with a descriptive error if the resolved path equals `os.ExpandEnv("$HOME/.config/herdr")`; CI installs herdr 0.9.0 (not 0.8.0) before running the suite.
- **Verification**: automated — the isolation guard is a `TestMain` assertion; CI pin in `.github/workflows/ci.yml` is updated to 0.9.0 as part of T0a.
- **Criticality**: must
- **See also** [HPO-R-001](#hpo-r-001), [HPO-R-003](#hpo-r-003)
