# `gh` CLI in sandboxes — design notes

**Branch**: develop  
**Status**: living document; authoritative for the gh injection mechanism and pinning procedure

---

## Goal

Ship `/usr/local/bin/gh` (or equivalent) into every sandbox with no change to user Containerfiles, no new mandatory guest mount, and no registry push. The mechanism must be safe to apply universally and work with non-root final USER, shell-less base images, and `--image` / default herdr paths — not just Containerfile builds.

Nexus's own image already carries `gh 2.101.0`. Other repos typically do not.

---

## Mechanism: host-side fetch, verify, inject

The primary mechanism is **host-side fetch + sha256 verify + inject into the image or rootfs**, performed before the sandbox boots. No curl, tar, or shell are required in the guest.

### Superseded design — hard cutover

The previous design used a bake-time `RUN` layer (`cred.SandboxTools` / `builder.RecipeWithSandboxTools`) that emitted `curl | sha256sum | tar` inside the builder VM. That approach is **removed**:

- Requires `curl`, `tar`, `sh`, and `sha256sum` in the build environment — absent from many minimal bases.
- The `RUN` instruction executes as the final `USER`, so non-root images fail the install.
- Shell-less bases (`scratch`, distroless) have no `/bin/sh`.
- Covered only `--file` builds; `--image`, `--rootfs`, and the default herdr path had no injection.

The `Optional` field, `{GOARCH}` placeholder, `cred.SandboxTools`, `cred.WithSandboxTools`, and `builder.RecipeWithSandboxTools` are removed. There is no migration shim.

---

## New code surface

### `internal/core/builder/toolcache`

Central package for host-side tool pins, fetch, verify, and caching.

**Pins** — `toolcache.GH()`:

| Field | Value |
|---|---|
| Tool | `gh` |
| Version | `2.101.0` |
| URL template | `https://github.com/cli/cli/releases/download/v{VERSION}/gh_{VERSION}_linux_{GOARCH}.tar.gz` |
| amd64 SHA256 | `9bca2d1c16825f109907a23307628a2f0698fbf99662b73a5cf0b020293072b8` |
| arm64 SHA256 | `b57e8063f18862647c9d22727c32e9da1b963f8bf9db648fe123a6975695640f` |

Source: `gh_2.101.0_checksums.txt` from the GitHub release page. Keys are Go arch names (`amd64`, `arm64`).

**`Fetcher.Fetch`**:

1. Download tarball from the pin URL.
2. sha256 verify **before** extraction — mismatch returns `ErrChecksumMismatch`; no cache entry is written.
3. Extract only `gh_<ver>_linux_<arch>/bin/gh` from the tarball.
4. Write to content-addressed cache: `<storeRoot>/tools/gh/<tarball-sha256>/gh` via atomic rename.

`storeRoot` = `$XDG_STATE_HOME/nexus` (falls back to `~/.local/state/nexus`).

**`StageTree`**: writes into a destination directory tree:
- `/usr/local/share/nexus-tools/gh/<ver>/bin/gh` (mode 0755)
- `/usr/local/bin/gh` — relative symlink pointing into the share dir

Symlink-safety: refuses to write through any existing symlink that resolves outside the destination root (prevents escape). Absolute symlinks from the image are re-rooted.

**`Digest(tools)`**: returns a stable hex digest over the set of tool pins; folded into cache keys wherever tools change what is baked.

### `internal/core/builder/toolcache.BuilderTreeDir`

Constant: `/opt/nexus-sandbox-tools/root`. This is the path inside the builder VM where the tool tree is staged for `--file` builds.

---

## Path A: `--file` and `.nexus/Containerfile` builds

1. **CLI** (`internal/cli/cmd_sandbox.go`): calls `resolveSandboxTools` once per `sandbox create`. On failure (offline, 404, checksum mismatch) logs a warning and continues — `gh` is omitted but the build is not aborted.
2. **Builder image** (`internal/core/builder/builderimage`): `EnsureBuilderImageWithTools` bakes the tool tree into the builder VM rootfs at `BuilderTreeDir`. The builder template filename gains a `-tools<digest>` suffix so template images rebuild when tools change.
3. **In-VM nexus-agent** (`internal/core/agent/buildkit_linux.go`): sets `SolveRequest.SandboxToolsDir` to `BuilderTreeDir`.
4. **Builder** (`internal/core/builder/buildkit.go`): `stageSandboxTools` copies `SandboxToolsDir` into the `nexusagent` named build context. `synthesizeDockerfileWithTools` prepends:

   ```dockerfile
   COPY --from=nexusagent nexus-sandbox-tools/ /
   ```

   before the agent `COPY`. `COPY` always runs as root, so non-root `USER` and shell-less bases both work.

5. **Fingerprint**: `fingerprintWithTools(BuildFingerprint(...), tools)` ensures the fingerprint changes when the gh pin is bumped or tools are opted out, triggering a rebuild.

**Skip-if-present**: not possible with `COPY` (no conditional). Nexus's own image ships `gh 2.101.0` at `/usr/bin/gh`; the injected binary lands at `/usr/local/bin/gh`, which precedes `/usr/bin` on PATH — same version, different path, effectively a shadow. An image with `/usr/local/bin/gh` gets it overwritten; use the opt-out directive if that is undesirable.

---

## Path B: `--image` and default herdr path

`cmd_herdr_plugin.go` (`herdrResolveWorktreeImageFromConfig`) and `--image` both reach `builderimage.PullAndCacheOCIWithTools`. After nexus-agent injection this function stages the tool tree into the extracted rootfs with `skipIfPresent=true`: skips when `gh` is already present at `/usr/local/bin`, `/usr/bin`, or `/bin`.

Image-cache tag: `builderimage.CacheTag(agentBytes, tools)` — includes the tool digest so cached images re-bake once when tools change.

The service layer plumbs this through `service.CreateAndBootOptions.SandboxTools` and `resolveExt4WithTools`.

---

## CLI resolution

`internal/cli/sandbox_tools.go`:
- `resolveSandboxTools`: called once per `sandbox create`; applies a per-fetch timeout; on any failure logs and returns a nil/empty tool set (create continues without gh).
- `fingerprintWithTools`: wraps `BuildFingerprint` with the tool digest for cache key derivation.

---

## Opt-outs

| Method | Effect |
|---|---|
| `# nexus:sandbox-tools-skip` in Containerfile | Skips gh injection for that build |
| `nexus sandbox create --no-sandbox-tools` | Skips gh for this invocation |
| `sandbox.tools.gh: false` in user-global `~/.config/nexus/config.yaml` | Skips gh for all sandboxes on this host |
| `sandbox.tools.gh: false` in repo `.nexus/config.yaml` | Skips gh for all sandboxes in that repo |

Config is opt-out only: `sandbox.tools.gh: true` never re-enables tools when a CLI flag (`--no-sandbox-tools`) or user-global config has already opted out (one-way ratchet). Either config layer can opt out; neither can override a more-restrictive setting set by the other. See `applyProjectConfig` and `applyUserGlobalConfig` in `internal/cli/cmd_sandbox.go`.

`# nexus:recipe-skip` drops the entire recipe layer (agent CLIs included) and always implies tools skip.

---

## Pinning

`KindTarball` forbids `FloatingVersion`; the version is always explicit.

### Bump procedure

1. Update `toolcache.GH()` version and SHA256 values (`gh_<ver>_checksums.txt` from the release page).
2. `BuilderTreeDir` template suffix and `CacheTag` both pick up the new digest — affected images and builder templates rebuild once on next create.

---

## GH_TOKEN interplay

Unchanged. `gh` reads `GH_TOKEN` / `GITHUB_TOKEN` from the environment. The guest already carries `GH_TOKEN` via `internal/core/service/secret.go` (`BuiltinGitHubEnv`), resolved and brokered by the egress credential broker. The git HTTPS credential helper (`internal/core/service/git_identity.go` `GuestGitCredentialHelperScript`) also reads `GH_TOKEN`; `gh auth setup-git` is neither needed nor run, and no `hosts.yml` is written.

`gh api graphql` calls go to `api.github.com/graphql`. Whether these succeed depends on the egress broker's GraphQL handling — separate in-flight work in `internal/core/perimeter/mitm`.

---

## Offline / air-gapped behaviour

- First create with no host cache and no network connectivity → logs warning, sandbox boots without gh.
- Subsequent creates reuse the host content-addressed cache fully offline.
- Integrity: unverified bytes are never staged. A checksum mismatch causes `Fetch` to return `ErrChecksumMismatch` and log; the create continues without gh.

---

## Deploy skew

Host CLI performs fetch/verify/stage and builder-image bake. The in-VM COPY emission lives in nexus-agent, installed alongside the CLI. A stale nexus-agent that predates `SolveRequest.SandboxToolsDir` ignores the baked tree — build succeeds without gh (graceful degradation). Builder template and image cache keys include the agent hash, so upgrading both triggers a rebuild.

---

## Scope: paths that receive gh injection

### Covered (gh injected)

| Entry point | Mechanism |
|---|---|
| `nexus sandbox create --file` / `.nexus/Containerfile` | Path A: builder COPY layer via `synthesizeDockerfileWithTools` |
| `nexus sandbox create --image` | Path B: `PullAndCacheOCIWithTools` + `StageTree` |
| herdr default (worktree image) | Path B: `herdrResolveWorktreeImageFromConfig` → `PullAndCacheOCIWithTools` |
| herdr `launch` (`runHerdrLaunch`) | `imageOptsSandboxTools` helper (wired R29 round 2) |
| `nexus run` | `imageOptsSandboxTools` helper (wired R29 round 2) |
| `orca create` / orca | `imageOptsSandboxTools` helper (wired R29 round 2) |
| MCP `CreateAndBoot` | `imageOptsSandboxTools` helper (wired R29 round 2) |
| MCP `RunEphemeral` | `imageOptsSandboxTools` helper (wired R29 round 2) |

### Not covered (gh absent or not guaranteed)

| Entry point | Reason |
|---|---|
| `nexus sandbox create --rootfs` | Raw ext4 passed through as-is; no injection point (Gap 1, open) |
| Digest-pinned images (`spec.Digest`) | Looked up by digest and used as-is, bypassing `PullAndCacheOCIWithTools` (Gap 2, open) |
| `nexus image build` | Goes through `BuildRequest` path, not wired to `resolveSandboxTools` (Gap 3, open) |
| Sandboxes built by stale nexus-agent | Old agent ignores `SolveRequest.SandboxToolsDir`; gh absent silently (Gap 4, open) |

For gap details and proposed fixes see `.groundwork/issues/sandbox-tools-uncovered-paths.md`.

---

## Testing strategy

Unit tests only — no live VM boots:

- `toolcache`: httptest server — tampered tarball rejected with `ErrChecksumMismatch`, no cache entry written; 404 propagates; missing tarball member errors; cache hit makes no HTTP request.
- `StageTree`: symlink-escape tests — absolute symlink in destination is re-rooted; symlink to outside destination root is refused.
- `builderimage`: `CacheTag` changes when tool digest changes; tools are injected into extracted rootfs; `skipIfPresent` skips when gh already present at expected paths.
- `builder`: `synthesizeDockerfileWithTools` emits `COPY --from=nexusagent` before agent COPY; `stageSandboxTools` populates named context correctly.
- `service`: `resolveExt4WithTools` routes through `PullAndCacheOCIWithTools` when tools non-nil; uses plain pull when tools nil or opted out.
- `cli`: `resolveSandboxTools` returns empty on fetch failure without erroring; `fingerprintWithTools` produces distinct fingerprints for different tool sets; opt-out flag suppresses tools.
- `config`: `sandbox.tools.gh: false` causes `resolveSandboxTools` to return nil.
