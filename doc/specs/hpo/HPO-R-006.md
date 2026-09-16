---
id: HPO-R-006
type: requirement
concept: C-HPO
criticality: must
verification: automated
status: active
origin_decision_ref: herdr-plugin-ootb#D-6
summary: "Install-time preflight warns on failing substrate checks and re-checks at first sandbox create."
---

## HPO-R-006 — Install-time preflight reports substrate problems with remediation {#hpo-r-006}

**When** `herdr plugin install IniZio/nexus3/plugins/herdr` completes on Linux, the install **shall** run `nexus3 doctor` and surface any failing check with its remediation text; **if** a check fails, the install **shall** register the plugin and print a warning rather than aborting, so that the doctor pane and plugin actions remain reachable; **when** the user subsequently runs `nexus3 herdr worktree-sandbox` or `nexus3 create`, the system **shall** re-run the preflight and fail fast with the same remediation text instead of producing a late VM-boot error.

- **Why** — aborting install on a failing substrate check strands a user who only needs `usermod -aG kvm` and a re-login: the plugin is absent and they cannot reach the doctor pane to find out why; failing fast at sandbox-create time with the same text closes the gap between install-time warning and first-use failure.
- **Fit criterion** — on a host where the `kvm` check fails: (a) install exits 0 and `herdr plugin list` shows the plugin enabled; (b) install output contains the check name and its remediation string; (c) `nexus3 herdr worktree-sandbox` exits non-zero within the first reconcile tick and its stderr contains the same remediation string without attempting VM boot.
- **Verification**: automated — `TestInstallPreflightWarnAndRegister` in the `herdr_live` suite mocks the KVM check to fail and asserts all three conditions; the sandbox-create re-check is asserted separately by `TestSandboxCreatePreflightFastFail`.
- **Criticality**: must
- **See also** [HPO-R-001](#hpo-r-001)
