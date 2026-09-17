---
id: HPO-R-002
type: requirement
concept: C-HPO
criticality: must
verification: manual
status: active
origin_decision_ref: herdr-plugin-ootb#D-5
summary: "macOS remote-client fresh install via one herdr plugin install command, no Go toolchain."
---

## HPO-R-002 — macOS remote-client one-command fresh install {#hpo-r-002}

**When** a macOS (arm64 or amd64) remote-client user with herdr ≥0.9.0 runs `herdr plugin install IniZio/nexus/plugins/herdr` on a host where no Go toolchain is present and `NEXUS_CLIENT` is unset, the `build.sh` bootstrap **shall** detect the non-Linux platform, download and verify the matching `nexus-client-darwin-{arm64|amd64}` release asset from the pinned release, install `nexus-client` to `~/.local/bin/nexus-client`, write the shim required by the `[[startup]]` hook, and leave the hook runnable — without requiring a Go toolchain or any manual step.

- **Why** — without a published `nexus-client` asset the non-Linux branch of `build.sh` exits 1 immediately; a macOS user cannot use the plugin at all, which is the primary remote-client use case.
- **Fit criterion** — on a macOS arm64 or amd64 host: after running the install command, `nexus-client herdr abi` prints `3` and exits 0, the shim is present, and `herdr plugin list` includes `nexus` with status enabled. Cross-compilation of `nexus-client-darwin-{arm64,amd64}` is CI-enforced via `GOOS=darwin CGO_ENABLED=0`; the end-to-end runtime proof is performed manually on the Mac minion (T8).
- **Verification**: manual — T1 enforces cross-build correctness in CI; T8 is a live end-to-end proof on the Mac minion performed by the operator after T1 ships.
- **Criticality**: must
- **See also** [HPO-R-001](#hpo-r-001)
