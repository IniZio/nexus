# herdr plugin test strategy

This is the standing contract for how the herdr+nexus3 integration is tested.
Future changes to the plugin, the manifest, or the shell scripts are held to
these four layers. Each layer names its concrete file or mechanism, states what
it can prove, and states what it cannot.

> **Human check — not in any layer**
> **Pane rendering and keybindings are outside all four layers and remain a
> human check.** No automated layer verifies that panes open with the correct
> placement, that tab labels are correct, that the UI is legible, or that
> keybindings fire the intended action. These must be checked by a person running
> a real herdr session.

---

## Standing rule — borrowed interfaces require Layer 4 probes

Any assertion about an interface nexus3 does **not** own requires a Layer 4
proof against the real thing. Fakes and stubs can only encode a belief about a
borrowed interface — they cannot detect that the interface changed.

Concretely:

- **herdr's CLI surface** — a flag name nexus3 passes, a subcommand nexus3
  calls, or an argv shape nexus3 constructs is not owned by nexus3. Layers 1–3
  stub the exec seam; they accept any argv without complaint. Layer 4 must
  assert the real herdr binary accepts the exact argv nexus3 sends.

- **herdr's JSON response shape** — a field name nexus3 parses from herdr
  output is not owned by nexus3. A herdr upgrade that renames a field is
  invisible to all three lower layers. Layer 4 must invoke the real command and
  assert every parsed field is present with the expected JSON type.

- **On-disk binding format** — a field nexus3 reads from a binding record
  written in a prior version must be present in records that predate the field.
  Layer 4 or a migration test must assert the backfill path works against real
  on-disk data.

**Adding a new herdr invocation requires a Layer 4 probe in the same change.**
Internal branch logic and in-process handler behaviour stay at Layers 1–3.

---

## Layer 1 — static manifest invariants

**Test file:** `internal/cli/herdr_manifest_test.go`

Reads `plugins/herdr/herdr-plugin.toml` without running anything and asserts
structural properties of the manifest:

| Test | What it proves |
|---|---|
| `TestHerdrManifest_EveryPaneIsReachable` | Every declared `[[panes]]` entry has at least one `[[actions]]` entry that opens it (or is in an explicit exemption list with a stated reason). |
| `TestHerdrManifest_EveryEntrypointResolves` | Every entrypoint named in an action either matches a pane id or is handled as a special case in `plugins/herdr/bin/open-pane.sh`. |

**What this layer proves:** A pane that no action can reach, or an action that
points at an entrypoint that nothing handles, is caught without running any
process.

**What this layer cannot prove:** That the scripts invoked by those actions
behave correctly; that the manifest parses correctly as a real herdr plugin; or
that any action does what its label says.

---

## Layer 2 — shell-layer drive-through

**Test file:** `internal/cli/herdr_scripts_test.go`

Actually **executes** `plugins/herdr/bin/pane.sh` and
`plugins/herdr/bin/open-pane.sh` against a pair of stub shims: one that records
every argv the scripts would pass to the `nexus3` binary, and one that records
every argv the scripts would pass to the `herdr` binary. The real scripts run;
only the final binary invocations are stubbed.

| Test | What it proves |
|---|---|
| `TestOpenPaneScript_LifecycleActionsResolveByWorkspaceID` | `pause`, `resume`, and `remove` forward `$HERDR_WORKSPACE_ID` as the subcommand argument — without it every action would fail with "sandbox ref required". |
| `TestOpenPaneScript_SplitOmitsWorkspace` | A split pane action carries `--plugin`, `--entrypoint`, `--placement`, and `--focus` but NOT `--workspace` — the server rejects `--workspace` for split and zoomed placements. |
| `TestOpenPaneScript_OverlayWithEnvOmitsWorkspace` | When `NEXUS3_WORKSPACE` is set with an overlay placement, `--env NEXUS3_WORKSPACE=…` is forwarded but `--workspace` is still absent — the server rejects `--workspace` for overlay regardless of the env var. |
| `TestOpenPaneScript_OmitsEnvFlagWhenWorkspaceUnset` | When `NEXUS3_WORKSPACE` is not set, `--env "NEXUS3_WORKSPACE="` is NOT forwarded (an empty ref is not the same as an absent one). |
| `TestOpenPaneScript_PassesEnvFlagWhenWorkspaceSet` | When `NEXUS3_WORKSPACE` is set, `--env NEXUS3_WORKSPACE=<value>` reaches herdr. |
| `TestPaneScript_ShellUsesResolvedGuestCwd` | The shell pane queries the guest for its working directory (`shell-cwd`) and passes it to `exec --cwd` — without this every shell opens in `/root`. |
| `TestPaneScript_ShellRefusesWithoutWorkspace` | The shell pane exits non-zero and names `NEXUS3_WORKSPACE` in the error when the variable is unset. |
| `TestPaneScript_RejectsUnknownSubcommand` | A manifest typo surfaces as a clear "unknown subcommand" error rather than a silently no-op pane. |
| `TestPaneScript_ProbesGuestNotHostForBash` | The shell pane asks the **guest** whether bash is available — not the host — so a macOS host (where bash is at `/bin/bash`, not `/usr/bin/bash`) does not silently degrade every guest shell. |
| `TestPaneScript_FallsBackToShWhenGuestLacksBash` | A minimal guest image without bash gets `/bin/sh` rather than an exec that closes the pane before the error is readable. |

**What this layer proves:** The contract between the manifest, the shell scripts,
and the CLI argument surfaces of both `nexus3` and `herdr` is correct for every
branching condition in the scripts.

**What this layer cannot prove:** That the real `nexus3` binary accepts the argv
the scripts send; that herdr routes actions to the correct script; or that the
scripts are syntactically valid in a guest environment (the stubs answer on the
host).

---

## Layer 3 — subcommand drive-through

**Test file:** `internal/cli/cmd_herdr_plugin_test.go`

Calls `runHerdrPlugin` directly — the same Go function that `nexus3
`herdr <sub>` dispatches to (and the deprecated `__herdr-plugin <sub>` alias) — using an in-memory service and a fake
driver. This exercises the real command router and Go handler logic without
forking a binary or touching disk.

Representative tests include `TestHerdrPluginABI`, `TestHerdrPluginContextCwd`,
`TestHerdrPluginWorkspaces_empty`, `TestHerdrPluginWorkspaces_nonEmpty`,
`TestHerdrPluginDoctor`, `TestHerdrPluginLogs`, `TestHerdrPlugin_unknownSubcommand`,
and `TestHerdrPlugin_noSubcommand`.

**What this layer proves:** The subcommand router dispatches to the correct
handler for every known subcommand. Error paths (unknown subcommand, missing
argument) produce the correct exit condition. Individual handlers produce
structurally correct output against a fake substrate.

**What this layer cannot prove:** That the binary-level argv produced by the
shell scripts (Layer 2) matches what this router expects; that the real
cloud-hypervisor driver behaves correctly; or that the output format is what
herdr parses on the other side of the pane boundary.

---

## Layer 4 — live herdr session

**Status: built**

**Test file:** `internal/cli/herdr_l4_live_test.go`

**Build tag:** `herdr_live` — excluded from the default `go test ./...` and
from CI. Run explicitly with:

```sh
TMPDIR=/tmp go test -count=1 -tags herdr_live ./internal/cli/ -run TestHerdrPlugin_L4
```

**Mechanism:** `TestHerdrPlugin_L4_BinaryVerb` builds the nexus3 binary, then
makes two probes:

1. **Direct exec** — calls `nexus3 herdr abi` as a subprocess and
   asserts stdout matches the ABI value declared in `plugins/herdr/abi`
   (currently `"2"`; the test reads the file rather than hardcoding the
   value, so this prose stays accurate when the ABI is next bumped).
   This is the mutation-sensitive primary assertion: renaming or deleting
   the `init()`-level `Command{Name: "herdr", ...}` registration causes
   the binary to exit 2 with `"error: unknown command: herdr"`, and this
   probe fails immediately.

2. **herdr pane smoke test** — creates a scratch workspace with a unique
   timestamp label, runs the binary inside it via `herdr pane run`, and waits
   for output via `herdr pane wait-output`. The scratch workspace is closed in
   `t.Cleanup` with a label-verified safety check that refuses to close any
   workspace whose label does not match. The four operator workspaces (`w6`,
   `w7`, `w8`, `wN`) are never touched.

**What this layer proves:** The `herdr` verb group is registered in the
shipped binary's CLI dispatch. The binary accepts `herdr abi` and
returns the ABI version. The verb registration in `cmd_herdr_plugin.go`'s
`init()` function cannot be silently deleted without this test failing.

**What this layer cannot prove:** That herdr routes plugin actions to the
correct script end-to-end (the full manifest→action→script→binary round-trip
is only verifiable by clicking an action in herdr). Correct pane rendering,
correct keybinding behaviour, and anything requiring visual inspection of
herdr's UI remain human checks.

**`TestHerdrPlugin_L4_Contract`** (file: `internal/cli/herdr_l4_contract_test.go`)
asserts the herdr CLI contract:

- **Command acceptance** — every `herdr` subcommand nexus3 invokes is verified
  to exist in the installed herdr binary. Read-only commands (`workspace list`,
  `worktree list --workspace <id>`) are invoked for real and exit 0 is
  asserted. Safe-mutating commands (`workspace rename`, `tab create`) are
  driven against a scratch workspace using the same timestamp-label + label-
  verified cleanup guard as `TestHerdrPlugin_L4_BinaryVerb`. All remaining
  mutating commands (`workspace close`, all `pane *`, `plugin pane open`) are
  verified structurally: `herdr <group> <subcmd> --help` is parsed to confirm
  each flag nexus3 passes appears in the usage text. A flag-name typo (e.g.
  `--workspace-id` vs `--workspace`) fails the structural probe.

- **Response shape** — `herdr worktree list --workspace <id>` and
  `herdr workspace list` are invoked for real and the JSON fields that
  `herdrParseWorktreeListForWorkspace` and the backfill parser depend on are
  asserted present with the expected types: `result.source.repo_key`,
  `result.source.source_workspace_id`, `result.worktrees[].branch`,
  `.path`, `.is_linked_worktree`, `.open_workspace_id`; and for workspace
  list, `worktree.repo_root` on each workspace entry that has a worktree. Field
  values are not asserted — only presence and type. A herdr upgrade that renames
  a field must fail this test.

**What `TestHerdrPlugin_L4_Contract` proves:** Every herdr command nexus3
currently issues is syntactically valid and accepted by the installed herdr
binary. Every JSON field the production parsers read is present in real herdr
output. Flag-name drift and response-shape changes that are invisible to all
three lower layers are caught here.

**What `TestHerdrPlugin_L4_Contract` cannot prove:** That the field values are
semantically correct; that herdr routes plugin actions to the correct script
end-to-end; or that optional flags behave as nexus3 expects when present.

**Residual gap (TBD-PD-40 remains open):** The layer proves the binary-level
verb exists but does not drive the full herdr manifest→action→shell-script→
binary chain in a single automated flow. That chain is proven piecewise by
layers 1–3; the final pane smoke test above confirms herdr can execute the
binary in a pane, but does not invoke a plugin action through the manifest.
TBD-PD-40 should remain open until an automated test can invoke
`herdr plugin action invoke` with the nexus3 plugin active and assert on
the resulting pane.

---

## Coverage boundaries

| Concern | Layer(s) | Not covered |
|---|---|---|
| Manifest structural correctness | 1 | — |
| Shell script wiring (argv, env, branching) | 2 | — |
| Subcommand router and handler logic | 3 | — |
| Binary verb registration (`herdr`) | 4 | — |
| herdr CLI command acceptance (every invocation) | 4 | — |
| herdr JSON response shape (parsed fields) | 4 | field values; optional-flag semantics |
| herdr manifest→action→script→binary round-trip | 4 (partial) | full chain via plugin action invoke |
| Pane rendering | none | human check |
| Keybindings | none | human check |
| herdr plugin API compatibility | none | human check |
