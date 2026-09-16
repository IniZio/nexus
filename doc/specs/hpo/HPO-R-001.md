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

**When** a Linux x86-64 user with herdr ≥0.9.0 runs `herdr plugin install IniZio/nexus3/plugins/herdr` on a host where `nexus3` is not yet installed, the `build.sh` bootstrap **shall** download and verify the pinned `nexus3-linux-amd64` release asset, install `nexus3` to `~/.local/bin/nexus3`, install `nexus3-guest-shell`, write the `[terminal] default_shell` entry to `config.toml`, and leave `herdr plugin list` reporting the plugin enabled — all without requiring any manual step from the user.

- **Why** — the pre-existing path requires the user to paste a `[terminal]` line into `config.toml` by hand; omitting that step leaves the guest shell unwired and every sandbox boot uses the wrong shell, which is a silent failure that a fresh user cannot diagnose.
- **Fit criterion** — after running the install command against a clean `HOME` (no prior `nexus3` binary, no prior `config.toml` entry), all of the following hold: `nexus3 version` exits 0, `nexus3-guest-shell` exists at `~/.local/bin/nexus3-guest-shell`, `herdr config check` exits 0, and `herdr plugin list` includes `nexus3` with status enabled.
- **Verification**: automated — `TestHerdrPluginInstall_FreshHome` in `make test-herdr-live` drives a real herdr 0.9.0 headless instance in an isolated session under a redirected config root and asserts all four conditions.
- **Criticality**: must
- **See also** [HPO-R-005](#hpo-r-005), [HPO-R-007](#hpo-r-007)
