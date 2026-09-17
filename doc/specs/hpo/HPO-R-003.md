---
id: HPO-R-003
type: requirement
concept: C-HPO
criticality: must
verification: automated
status: active
origin_decision_ref: herdr-plugin-ootb#D-3
summary: "Re-running install upgrades older releases; dev and newer builds are preserved with a status message."
---

## HPO-R-003 — Idempotent upgrade; dev and newer builds preserved {#hpo-r-003}

**When** `herdr plugin install IniZio/nexus/plugins/herdr` is re-run on a host that already has `nexus` installed, `build.sh` **shall** upgrade an older release binary to the pinned tag, leave a `-dev` build or any binary newer than the pin untouched, and print a message stating which action was taken (upgraded, skipped-dev, skipped-newer).

- **Why** — without the skip-if-newer guard, re-running install during active development overwrites a locally built `-dev` binary with the pinned release, breaking the developer's working tree silently; without the message, the user cannot tell whether the re-run did anything.
- **Fit criterion** — given three scenarios exercised by the test: (a) installed version older than pin → binary replaced and message contains "upgraded"; (b) installed version is a `-dev` suffix build → binary unchanged and message contains "skipped"; (c) installed version newer than pin → binary unchanged and message contains "skipped". All three pass in `TestHerdrPluginUpgrade` under `make test-herdr-live`.
- **Verification**: automated — `TestHerdrPluginUpgrade` in the `herdr_live` suite exercises all three skip/upgrade branches against a local `httptest` release server seeded with `NEXUS_RELEASE_BASE_URL`.
- **Criticality**: must
- **See also** [HPO-R-004](#hpo-r-004)
