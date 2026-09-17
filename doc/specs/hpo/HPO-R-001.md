---
id: HPO-R-001
type: requirement
concept: C-HPO
criticality: must
verification: automated
status: active
origin_decision_ref: herdr-plugin-ootb#D-1
summary: "Linux x86-64 fresh install via one herdr plugin install command with no manual step."
---

## HPO-R-001 — Linux x86-64 one-command fresh install {#hpo-r-001}

**When** a Linux x86-64 user with herdr ≥0.9.0 runs `herdr plugin install IniZio/nexus/plugins/herdr` on a host where `nexus` is not yet installed, the `build.sh` bootstrap **shall** download and verify the pinned `nexus-linux-amd64` release asset, install `nexus` to `~/.local/bin/nexus`, install `nexus-guest-shell`, write the `[terminal] default_shell` entry to `config.toml`, and leave `herdr plugin list` reporting the plugin enabled — all without requiring any manual step from the user.

- **Why** — the pre-existing path requires the user to paste a `[terminal]` line into `config.toml` by hand; omitting that step leaves the guest shell unwired and every sandbox boot uses the wrong shell, which is a silent failure that a fresh user cannot diagnose.
- **Fit criterion** — after running the install command against a clean `HOME` (no prior `nexus` binary, no prior `config.toml` entry), all of the following hold: `nexus version` exits 0, `nexus-guest-shell` exists at `~/.local/bin/nexus-guest-shell`, `herdr config check` exits 0, and `herdr plugin list` includes `nexus` with status enabled.
- **Verification**: automated — `TestHerdrPluginInstall_FreshHome` in `make test-herdr-live` drives a real herdr 0.9.0 headless instance in an isolated session under a redirected config root and asserts all four conditions.
- **Criticality**: must
- **See also** [HPO-R-005](#hpo-r-005), [HPO-R-007](#hpo-r-007)
