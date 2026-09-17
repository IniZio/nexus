# motive: rename-nexus3-to-nexus

## Objective

The product is called `nexus`, not `nexus3`, everywhere a user or the toolchain sees the name: Go module path, binaries, CLI root verb, host/guest state paths, env vars, network interface prefixes, herdr/Claude/orca plugin ids, docs site, spec concept id, CI release assets and container image. The operator's host is cut over to the renamed product with a clean break (old state deleted), and the rename is live-proven through herdr.

Operator intent (verbatim, 2026-09-17): "rename the whole thing to nexus instead of nexus3".

Scope class: **Complex** — ~7k lines across ~680 files, plus host-side state and running processes.

## Decision Log

- **D-1** (operator, 2026-09-17) Module path `github.com/IniZio/nexus3` → `github.com/IniZio/nexus`. Operator renames the GitHub repo; GitHub redirects cover the old remote until it is updated.
- **D-2** (operator, 2026-09-17) The local checkout stays at `~/magic/nexus3`. `~/magic/nexus` is an unrelated scratch dir; 25 worktrees, a live supervisor with 4 virtiofsd mounts, herdr `session.json`, the `~/.claude/plugins/nexus3` symlink and the orchestrating session all pin the current path. Moving it is out of scope.
- **D-3** (operator, 2026-09-17) Host state is a **clean break, deleted**: `~/.local/state/nexus3`, `~/.config/nexus3`, `~/.cache/nexus3`, `~/.local/share/nexus3`, `/var/lib/nexus3`, `/run/nexus3` are removed after live sandboxes are stopped through herdr verbs. No migration shim. Existing sandboxes and cached guest images are discarded (guest images bake `/etc/nexus3/*` and must be rebuilt anyway).
- **D-4** (operator, 2026-09-17) `~/.local/bin/nexus` (87 MB, Jul 24, old-nexus product) is backed up to `~/.local/bin/nexus.old-product` and overwritten by the new build.
- **D-5** (orchestrator default) Env vars `NEXUS3_*` → `NEXUS_*` with no aliasing; tap/bridge prefixes `nx3{g,h,b}-` → `nx{g,h,b}-`; spec concept `C-NEXUS3` → `C-NEXUS` (sub-concepts unchanged); release assets `nexus-linux-amd64`, `nexus-client-*`; image `ghcr.io/inizio/nexus-base`; stamp file `plugins/herdr/nexus-version`; plugin ids `nexus` (`nexus@nexus`, `/nexus:nexus-init`).
- **D-6** (orchestrator default) Historical records are not rewritten: `.groundwork/**`, `CHANGELOG.md`, `plugins/claude/evals/**`. The Claude project-memory directory keeps its path-derived name because the checkout path is unchanged (D-2).

## Replacement rules (shared by every slice)

1. `github.com/IniZio/nexus3` → `github.com/IniZio/nexus`
2. `nx3g-`/`nx3h-`/`nx3b-` → `nxg-`/`nxh-`/`nxb-`
3. `NEXUS3` → `NEXUS`
4. `Nexus3` → `Nexus`
5. `nexus3` → `nexus`
Path renames via `git mv`.

## Acceptance

- AC-1 `rg -i nexus3` over the repo, excluding `.git .claude .groundwork CHANGELOG.md plugins/claude/evals docs/site/node_modules third_party`, returns 0 lines.
- AC-2 `make build`, `make vet`, `make test` (full untagged suite) exit 0.
- AC-3 `doc/specs/_generated/index.json` carries `C-NEXUS`; the godog spec suite passes.
- AC-4 Operator host: no `nexus3` processes, no old state dirs, `~/.local/bin/nexus` is the new build, `nexus-agent` is static, Claude plugin `nexus@nexus` installed and `nexus3@nexus3` removed, herdr plugin `nexus` installed.
- AC-5 Live proof through herdr: a worktree sandbox is created with the renamed verb, an exec inside succeeds, the guest identifies `nexus-agent`, host state lives under `~/.local/state/nexus`, sandbox torn down via herdr.

## Out of scope

Moving the checkout directory (D-2); renaming the GitHub repo (operator action); publishing a release under the new asset names (happens on next `main` push via semantic-release); rewriting historical records (D-6).

## Run ledger

`.groundwork/runs/03439984-cc47-4b5a-a454-b79bba7a4516.json` — 8 slices, 4 waves.
