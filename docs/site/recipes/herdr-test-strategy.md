---
title: "herdr plugin test strategy"
description: "Four-layer test contract for the herdr+nexus3 integration — what each layer proves and what it cannot"
---

# herdr plugin test strategy

This is the standing contract for how the herdr+nexus3 integration is tested.
Future changes to the plugin, the manifest, or the shell scripts are held to
these four layers. Each layer names its concrete file or mechanism, states what
it can prove, and states what it cannot.

::: info Human check — not in any layer
**Pane rendering and keybindings are outside all four layers and remain a
human check.** No automated layer verifies that panes open with the correct
placement, that tab labels are correct, that the UI is legible, or that
keybindings fire the intended action. These must be checked by a person running
a real herdr session.
:::

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
| `TestOpenPaneScript_GenericPaneCarriesPlacementAndWorkspace` | A pane action calls herdr with the correct `--plugin`, `--entrypoint`, `--placement`, `--workspace`, and `--focus` flags. |
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
__herdr-plugin <sub>` dispatches to — using an in-memory service and a fake
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

## Layer 4 — live herdr session <Badge type="tip" text="built" />

**Test file:** `internal/cli/herdr_l4_live_test.go`

**Build tag:** `herdr_live` — excluded from the default `go test ./...` and
from CI. Run explicitly with:

```sh
TMPDIR=/tmp go test -count=1 -tags herdr_live ./internal/cli/ -run TestHerdrPlugin_L4
```

**Mechanism:** `TestHerdrPlugin_L4_BinaryVerb` builds the nexus3 binary, then
makes two probes:

1. **Direct exec** — calls `nexus3 __herdr-plugin abi` as a subprocess and
   asserts stdout is `"1"`. This is the mutation-sensitive primary assertion:
   renaming or deleting the `init()`-level
   `Command{Name: "__herdr-plugin", ...}` registration causes the binary to
   exit 2 with `"error: unknown command: __herdr-plugin"`, and this probe
   fails immediately.

2. **herdr pane smoke test** — creates a scratch workspace with a unique
   timestamp label, runs the binary inside it via `herdr pane run`, and waits
   for output via `herdr pane wait-output`. The scratch workspace is closed in
   `t.Cleanup` with a label-verified safety check that refuses to close any
   workspace whose label does not match. The four operator workspaces (`w6`,
   `w7`, `w8`, `wN`) are never touched.

**What this layer proves:** The `__herdr-plugin` verb is registered in the
shipped binary's CLI dispatch. The binary accepts `__herdr-plugin abi` and
returns the ABI version. The verb registration in `cmd_herdr_plugin.go`'s
`init()` function cannot be silently deleted without this test failing.

**What this layer cannot prove:** That herdr routes plugin actions to the
correct script end-to-end (the full manifest→action→script→binary round-trip
is only verifiable by clicking an action in herdr). Correct pane rendering,
correct keybinding behaviour, and anything requiring visual inspection of
herdr's UI remain human checks.

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
| Binary verb registration (`__herdr-plugin`) | 4 | — |
| herdr manifest→action→script→binary round-trip | 4 (partial) | full chain via plugin action invoke |
| Pane rendering | none | human check |
| Keybindings | none | human check |
| herdr plugin API compatibility | none | human check |
