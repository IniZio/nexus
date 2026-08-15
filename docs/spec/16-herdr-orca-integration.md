# 16 — Herdr and Orca Integration

*Purpose: how herdr and orca consume nexus3 today, the three distinct sandbox-creation entry
points, the surface-duplication problem they create, and what is planned but not yet built.
Evidence traces to code line numbers, the B3 slice findings, and the motive charter for the
`nexus3-parallel-dev-pr-flow` milestone.*

*This area carries the most historically aspirational claims in the spec corpus. doc 14 section
on herdr/orca was flagged as aspirational; this document replaces it for the integration
description. Every claim here names its source. Where a capability is planned but not built, it
is under an explicit **"Not yet built"** heading.*

*Accuracy rule: every code citation was verified by `grep -n` against the live source on
2026-08-15. The symbol name is the stable key; the line number is a convenience hint that rots
on edits.*

---

## 1 — The three sandbox-creation entry points (current architecture)

Today there are **three independent code paths** that each implement sandbox creation and boot:

| Surface | Entry function | File | `kernelPathFor()` at |
|---|---|---|---|
| herdr plugin `launch` subcommand | `herdrPluginLaunch` (dispatched from `case "launch":`) | `cmd_herdr_plugin.go:99` | `cmd_herdr_plugin.go:576` |
| herdr plugin `space-create-from-file` | `herdrPluginSpaceCreateFromFile` | `cmd_herdr_plugin.go:124` | (delegates to sandbox create path) |
| MCP server | `(m *mcpService).CreateAndBoot` | `cmd_mcp.go:45` | `cmd_mcp.go:53` |
| Orca CLI | `orcaCreate` | `cmd_orca.go:450` | `cmd_orca.go:520` |

Three of these four entry points call `kernelPathFor()` independently. The fourth (`space-create-from-file`) calls into the same `sandbox create` path as the CLI.

This architecture has a concrete consequence, not a theoretical concern: a kernel-preflight fix
applied to `cmd_sandbox.go` (the CLI `sandbox create` path) does **not** propagate to
`cmd_herdr_plugin.go` (the `launch` subcommand) or `cmd_mcp.go` (the MCP `CreateAndBoot`). Slice
B3 documented this gap; slices B6 and I1 address it.

**Slice I1 will establish a single canonical creation path** that all surfaces adapt to, with a
surface-parity check asserting no verb exists in one surface that is not reachable from the
canonical API (N-AC4, REQ-SUR-004). Until I1 lands, the three-entry-point architecture is the
ground truth. **I1 is NOT YET BUILT.**

---

## 2 — herdr plugin surface

### Plugin manifest and entry point

The herdr plugin manifest is at `plugins/herdr/herdr-plugin.toml`. Key fields:

- `min_herdr_version = "0.7.4"` — herdr binary floor; the plugin will not load against older
  versions.
- `platforms = ["linux", "macos"]` — declared, though the macOS path requires `nexus3-vzd`
  (doc 12), which is backlogged.
- Pane scripts: `plugins/herdr/bin/pane.sh` dispatches to subcommands.

### `__herdr-plugin launch` — the agent sandbox path

`nexus3 __herdr-plugin launch` (`cmd_herdr_plugin.go:99`) boots an agent sandbox and execs the
specified command inside the guest over a PTY. It is the primary path by which herdr opens a
Claude-in-guest session. The `--agent-egress` flag wires `AllowedHosts` via `WireClaudeEgress`
(`internal/core/service/create.go:246`), which sets it to `AgentEgressHosts()` (containing only
`api.anthropic.com` and `platform.claude.com`). Without `--agent-egress`, the sandbox has the
plain launch path: no perimeter, no `AllowedHosts` filter (`cmd_herdr_plugin.go:558`).

This command is **private** — the double-underscore prefix marks it as an internal implementation
detail of the herdr plugin, not a user-facing verb.

### `__herdr-plugin space-create-from-file` — the Dockerfile-based workspace path

`nexus3 __herdr-plugin space-create-from-file` (`cmd_herdr_plugin.go:124`) interactively prompts
for a build context and Containerfile, invokes `nexus3 sandbox create --file <dockerfile>`, and
opens a guest-shell herdr space. This is the path herdr's `pane.sh` calls when the operator
selects "new workspace from file" in the TUI.

### `__herdr-plugin space-create` — the pre-built image workspace path

`nexus3 __herdr-plugin space-create` (`cmd_herdr_plugin.go:110`) creates a workspace from a
pre-built image reference. Less commonly used than `space-create-from-file`.

---

## 3 — Orca integration

### The abandoned GUI-composer path

The Orca GUI-composer path — creating a nexus3 VM via Orca's "Run on" workspace recipe picker —
was **abandoned as the primary route** and is not viable today. Two independent blockers made it
dead:

1. The remote Orca client's "Run on" picker does **not** surface VM recipes. The pilot tested
   this against the real Orca AppImage v1.4.179 (headless, `vm recipe doctor --provision`).
2. The host is **headless to the operator**: there is no GUI session in which the Orca composer
   can be interacted with.

Do not describe the Orca GUI-composer path as viable. It does not work.

### What remains real

Two Orca-adjacent assets are real and CLI-proven:

1. **CLI recipe** (`nexus3 recipe`): `vm recipe doctor --provision` was proven green against the
   real Orca AppImage (milestone orchestrator proof, 2026-08-10). This is a CLI operation, not a
   GUI one.
2. **Direct-SSH fallback**: the Orca workspace `proxyCommand` path (`cmd_orca.go:129`,
   `buildOrcaConnectionJSON`) wires up SSH over vsock so that a workspace opened via direct SSH
   can reach the guest. This is CLI-accessible and functions independently of the GUI composer.

### `orcaCreate` — the Orca creation path

`orcaCreate` (`cmd_orca.go:450`) is invoked when Orca calls `nexus3 orca create` at workspace
creation time. It:

1. Reads `ORCA_VM_INSTANCE_ID`, `ORCA_REPO_PATH`, `ORCA_WORKSPACE_NAME`, and related env vars
   from the Orca environment (`readOrcaEnv`, `cmd_orca.go:88`).
2. Generates or loads a per-instance SSH keypair for the workspace.
3. Calls `service.CreateAndBoot` with a `WorkspaceSpec` derived from the Orca env vars
   (`orcaWorkspaceSpec`, `cmd_orca.go:161`).
4. Builds and returns `orcaCreateResult` containing the SSH `proxyCommand` that Orca uses for
   subsequent connections.

`kernelPath := kernelPathFor()` is called inside `orcaCreate` at `cmd_orca.go:520`. Like the
other entry points, it is not guarded by a preflight check (B3 gap).

### What git hosts Orca can reach

`gitHostsFromURL` (`cmd_orca.go:178`) derives the git host(s) from the Orca workspace's repo URL
and adds them to `AllowedHosts` so the in-guest agent can clone or fetch. This is the only
product code path that programmatically adds hosts other than Anthropic's to `AllowedHosts`. It
adds the repo's git host (e.g. `github.com`) — not for push, only for clone/fetch by the guest
agent. An in-guest `git push` is not gated by `AllowedHosts` alone; the push credential would
also need to be present in the guest. In the nexus3-parallel-dev-pr-flow motive, in-guest push
is prohibited (D-PD-01); the Orca path's git host permission is scoped to read-only clone/fetch
only.

---

## 4 — MCP surface (current)

The MCP server (`internal/mcp/server.go`) exposes **seven tools** today
(`internal/mcp/server.go:23-29`):

| Tool | Description | Line |
|---|---|---|
| `sandbox_create` | Mint a new sandbox record; optionally boots it | `server.go:157` |
| `sandbox_list` | List all sandbox records (no args) | `server.go:198` |
| `sandbox_start` | Transition a created or stopped sandbox to running | `server.go:210` |
| `sandbox_stop` | Stop a running sandbox | `server.go:225` |
| `sandbox_pause` | Pause a running sandbox | `server.go:240` |
| `sandbox_resume` | Resume a paused sandbox | `server.go:253` |
| `sandbox_remove` | Remove a sandbox | `server.go:~265` |

The MCP surface is exposed via `nexus3 mcp` (`cmd_mcp.go:101`), stdio-only, no network listener.
The `modelcontextprotocol/go-sdk` library handles the MCP protocol; nexus3 owns the policy
(`internal/mcp` package).

### What the MCP surface cannot do today

The MCP server has no tool for launching an agent inside a sandbox. An MCP client can create and
start a sandbox but cannot cause a Claude agent to run inside it via MCP. That is the scope of
slice I2 (see section 5 below).

The `sandbox_create` MCP tool call path has the same kernel-preflight gap as the herdr `launch`
and orca paths (`cmd_mcp.go:53`). A create call with `NEXUS3_KERNEL_PATH` unset will attempt a
workspace capture before failing on the kernel (B3 gap, tracked under I1's preconditions).

---

## Not yet built

This section covers integration features that appear in the motive charter but have no product
code today.

### I1 — Canonical creation surface

Slice I1 will produce:
- `docs/spec/17-surface-contract.md` (the normative description of the canonical surface)
- `internal/mcp/envelope.go` (uniform MCP response envelope)
- `internal/core/service/ephemeral.go` (ephemeral one-call exec with guaranteed cleanup)
- `internal/cli/cmd_run.go` (`nexus3 run` command)
- `internal/cli/surface_parity_test.go` (automated check that CLI and MCP verb sets match)

Until I1 lands, the three independent creation paths remain, and there is no automated check for
surface drift. The surface parity requirement (N-AC4, REQ-SUR-004) is not yet asserted by any
test.

### I2 — MCP parity backfill and public `nexus3 agent launch`

Slice I2 will:
1. Backfill the MCP surface to match the CLI surface (adding the verbs that CLI has but MCP does
   not, as enumerated by I1's parity check).
2. **Promote `nexus3 agent launch` as a public command.** Today `__herdr-plugin launch` is the
   only agent-launch entry point, and it is private. I2 will add a public `nexus3 agent launch`
   verb with the private form delegating to it. This makes the agent-launch capability accessible
   to any surface, not just herdr.

Neither I2 change exists today. The public `nexus3 agent launch` command does not appear in
`root.go` or any CLI source file as of 2026-08-15.

### In-guest claude authentication via MCP

No MCP tool today mints or delivers a Claude credential into a guest. The credential seeding
path (`WireClaudeEgress`, `internal/core/service/create.go:229`) is available to surfaces that
call `CreateAndBoot` with the right options, but the MCP `sandbox_create` tool does not expose
these options. Until I2 lands, an MCP client that wants agent egress must use the herdr plugin
path or the CLI directly.

### The Orca workspace-creation GUI path

This is not "planned" — it is **permanently blocked** by the picker not surfacing VM recipes.
Unless the Orca client adds VM recipe support, this path is unavailable. The CLI recipe and
direct-SSH paths are the operational alternatives.

---

## 5 — Summary: what is real vs what is planned

| Capability | Status | Reference |
|---|---|---|
| herdr plugin loads (min v0.7.4) | **Real** | `herdr-plugin.toml`, milestone proof 2026-08-10 |
| `__herdr-plugin launch` agent sandbox | **Real** | `cmd_herdr_plugin.go:99` |
| `__herdr-plugin space-create-from-file` | **Real** | `cmd_herdr_plugin.go:124` |
| Orca CLI recipe (`nexus3 recipe`) | **Real** | proven 2026-08-10 against v1.4.179 |
| Orca direct-SSH fallback | **Real** | `cmd_orca.go:129` |
| `orcaCreate` called by Orca at workspace creation | **Real** | `cmd_orca.go:450` |
| MCP 7 sandbox lifecycle tools | **Real** | `internal/mcp/server.go:23-29` |
| Orca GUI-composer VM recipe path | **Abandoned** | picker does not surface VM recipes |
| Canonical single creation path (I1) | NOT YET BUILT | slice I1, `docs/spec/17-surface-contract.md` |
| Public `nexus3 agent launch` | NOT YET BUILT | slice I2 |
| MCP agent-launch tool | NOT YET BUILT | slice I2 |
| MCP parity with CLI surface | NOT YET BUILT | slice I2 |
| Kernel preflight on herdr/MCP/orca paths | NOT YET BUILT | slice B6, precondition of I1 |

---

*Sources: `cmd_herdr_plugin.go` (lines 99, 110, 124, 516, 558, 576), `cmd_mcp.go` (lines 45,
53, 101), `cmd_orca.go` (lines 88, 129, 161, 178, 450, 520), `internal/mcp/server.go` (lines
23-29, 157, 198, 210, 225, 240, 253), `internal/core/service/create.go` (lines 229, 246),
`plugins/herdr/herdr-plugin.toml`; motive charter decisions D-PD-01, D-PD-16, ACs N-AC1, N-AC4,
I1-AC1–AC4; doc 14 (compose-monorepo proof, herdr/orca gap map section); memory note
`nexus3-orca-workspace-integration.md`.*
