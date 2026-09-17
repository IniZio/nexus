---
id: HPO-R-004
type: requirement
concept: C-HPO
criticality: must
verification: automated
status: active
origin_decision_ref: herdr-plugin-ootb#D-3
summary: "ABI or version skew prints the exact herdr plugin install update command verbatim."
---

## HPO-R-004 — ABI/version skew reported with the exact update command {#hpo-r-004}

**When** `nexus herdr doctor` or the plugin `[[startup]]` hook detects that the installed `nexus` binary version or herdr ABI does not match the pinned release, the system **shall** print the exact command `run: herdr plugin install IniZio/nexus/plugins/herdr` verbatim in its output.

- **Why** — a skew notice that omits the remediation command leaves the user searching for the right incantation; a wrong or outdated command in the notice trains users to run the wrong thing. The verbatim string must be machine-checkable so it cannot silently drift.
- **Fit criterion** — with the installed binary reporting a version or ABI that differs from the expected pin, both `nexus herdr doctor` stdout and the `[[startup]]` hook output contain the literal string `herdr plugin install IniZio/nexus/plugins/herdr`. A test asserts the literal presence of this string.
- **Verification**: automated — `TestHerdrDoctorSkewNotice` and the startup-hook output assertion in the `herdr_live` suite check for the exact string in both code paths.
- **Criticality**: must
- **See also** [HPO-R-003](#hpo-r-003)
