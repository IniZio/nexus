# Prior-Art Brief: CLI & API Surface of Reference Projects

**Scope**: Exact CLI/API surface for labels, selectors, batch exec, exec, lifecycle,
artifact handling, and response envelope shape across three reference projects.
Excludes internal implementation detail not observable from public docs/source.

**Sources consulted** (2026-08-15):
- clawk: `github.com/clawkwork/clawk` README + `docs/commands.md` + `docs/ticket-mode.md`
- OpenShell: `github.com/NVIDIA/OpenShell` README + `docs.nvidia.com/openshell/latest/`
- microsandbox: `docs.microsandbox.dev` (labels, overview, SDK, CLI) + `github.com/superradcompany/microsandbox-mcp` README

---

## 1. clawk (`github.com/clawkwork/clawk`)

clawk is a per-developer microVM manager: one sandbox per working directory (or one
per ticket in ticket mode). Its mental model is identity-by-path, not
identity-by-label.

### Labels

**NO LABEL SYSTEM.** The words `--label`, `--tag`, `--filter`, `selector`, and
`group` do not appear anywhere in `docs/commands.md` or the README as CLI flags.
Sandboxes are addressed by name (auto-derived from the directory or ticket).
[VERIFIED from `raw.githubusercontent.com/clawkwork/clawk/main/docs/commands.md`]

### Selectors

**NONE.** No multi-sandbox selection mechanism. Every lifecycle command takes an
optional `[<name>]` positional, defaulting to the sandbox for the current directory.
There is no way to address a set of sandboxes in one invocation.
[VERIFIED from `docs/commands.md`]

### Batch Operations

**NONE.** No batch exec or fleet lifecycle operations. Commands operate on one
sandbox at a time. "Don't build it" is the effective signal from clawk.
[VERIFIED from `docs/commands.md`]

### Exec (single sandbox)

`clawk debug vshell [<name>] [-- cmd]` — raw vsock shell escape hatch, with an
optional command to execute non-interactively. Not a first-class exec command.
`clawk run <runner>` — attaches an AI runner (claude, codex, opencode, shell) to the
sandbox; this is agent-attach, not command execution.
[VERIFIED from `docs/commands.md`]

### Lifecycle (full verb list)

```
clawk                      # boot + attach (CWD mode)
clawk list                 # all sandboxes
clawk status [<name>] [--json]
clawk attach [<name>]      # reattach runner, boot if stopped
clawk up [<name>]          # boot a stopped sandbox
clawk down [<name>]        # stop (discards snapshot)
clawk pause [<name>]       # freeze vCPUs (memory stays resident)
clawk resume [<name>]      # continue paused or snapshotted sandbox
clawk snapshot [<name>]    # suspend-to-disk (hibernate)
clawk destroy [<name>]     # remove VM; host-side state persists

clawk run <runner> [-- args]   # launch agent in sandbox
clawk forward add <name> <port>
clawk forward remove <name> <port>
clawk network allow <name> <host>
clawk network deny <name> <host>
clawk network denials [--json]

clawk work <ticket>        # ticket mode: worktree-per-repo sandbox
clawk pr <ticket>          # push branches + open cross-linked PRs

clawk auth set-token
clawk system info [--json]
clawk system df [--json]
clawk system prune [--image]
clawk debug dump [<name>]
clawk debug vshell [<name>] [-- cmd]
clawk serial add/list/remove
```
[VERIFIED from `docs/commands.md` + README]

### Artifact / Preview Handling

`clawk pr <ticket>` — pushes all worktree branches and opens one GitHub PR per
changed repo (idempotent; `--draft`, `--base` flags). This is a **workflow verb**
in ticket mode, not a generic artifact-extraction primitive. No mechanism for
extracting a built artifact from a sandbox guest filesystem.
[VERIFIED from README + `docs/ticket-mode.md`]

### Response Envelope (JSON/API)

`clawk status --json` and other read commands support `--json`. The JSON output
carries a `schema` field; within a schema version, changes are additive only
(no removals, renames, or type changes). A breaking change bumps the schema number.
No `{ok, data, error, truncated}` envelope; that shape is not part of clawk's design.
[VERIFIED from `docs/commands.md`]

---

## 2. NVIDIA OpenShell (`github.com/NVIDIA/OpenShell`)

OpenShell is a gateway-mediated sandbox runtime for AI agents, oriented around
policies and credential providers rather than developer workflow. Its mental model
is security boundary + policy enforcement.

### Labels

**NO LABEL SYSTEM.** No `--label`, `--tag`, `--filter`, or `--selector` flags appear
in any OpenShell CLI documentation or the GitHub README. Sandboxes are addressed by
name. There is a rich policy YAML system (`openshell policy set <name> --policy
file.yaml`) but this controls security policy, not grouping metadata.
[VERIFIED from `github.com/NVIDIA/OpenShell` README + `docs.nvidia.com/openshell/latest/` quickstart + tutorial]

### Selectors

**NONE.** Sandboxes are addressed by name. No multi-sandbox selection mechanism
documented anywhere.
[VERIFIED from README key-commands table + quickstart]

### Batch Operations

**NONE.** No batch exec or fleet lifecycle. Every command takes a single sandbox
name or defaults to the most recent. "Don't build it" is the effective signal.
[VERIFIED from README + all accessible docs pages]

### Exec (single sandbox)

`openshell sandbox connect [name]` — SSH into a running sandbox (interactive shell).
No non-interactive `exec <name> -- cmd` verb exposed in the CLI.
[VERIFIED from README key-commands table]

### Lifecycle (full verb list — documented)

```
openshell sandbox create -- <agent>          # create + launch agent
openshell sandbox create --from base         # create from base image
openshell sandbox create --provider <p> -- <agent>
openshell sandbox create --no-keep -- <agent>  # auto-delete on agent exit
openshell sandbox connect [name]             # SSH into running sandbox
openshell sandbox list                       # list all sandboxes

openshell policy set <name> --policy file.yaml
openshell policy get <name>
openshell provider create --type <type> --from-existing
openshell inference set --provider <p> --model <m>
openshell logs [name] --tail
openshell term                               # real-time TUI debugger
```

Destroy/stop/remove verbs are not documented in the README key-commands table or the
quickstart. They almost certainly exist but the exact flag spelling is UNKNOWN.
[VERIFIED for listed verbs; UNKNOWN for destroy/stop/remove]

### Artifact / Preview Handling

**NONE documented.** No artifact extraction, bundle, or download mechanism found
anywhere in the OpenShell docs.
[VERIFIED from README + quickstart + tutorial]

### Response Envelope (JSON/API)

**UNKNOWN.** No `--json` flag or MCP/API response envelope documented in accessible
pages. OpenShell exposes an MCP server at `docs.nvidia.com/openshell/_mcp/server`
but its schema is not accessible without gateway connection.

---

## 3. microsandbox (`docs.microsandbox.dev` / `github.com/superradcompany/microsandbox`)

microsandbox is the most feature-complete reference. It exposes a CLI (`msb`), SDKs
in Rust/TypeScript/Python/Go, and an MCP server.

### Labels

**YES — full label system.** Labels are `key=value` pairs. They are:
- Repeatable: `--label KEY=VALUE` may appear multiple times on a single command
- A bare key is allowed: `--label gpu` stores `gpu=""`
- Docker-semantic: follows Docker's label convention
- Reserved prefixes rejected: `sandbox.`, `microsandbox.`, `service.`
- OCI image labels imported automatically at create time; user labels override on collision

CLI flag spelling: `--label KEY=VALUE` (repeatable)
Remove flag spelling: `--label-rm KEY` (on `msb modify`)

**At create time:**
```bash
msb create python --name worker \
  --label app=engine \
  --label user.id=alice
```

**Modify while running:**
```bash
msb modify worker --label tier=web --label-rm stale
```

**Domain object**: Labels are stored as `map[string]string`. Evidence from SDKs:
- Python: `labels={"app": "engine", "user.id": "alice"}` (dict)
- Go: `map[string]string{"app": "engine"}`
- TypeScript: `labels: { tier: "web" }` (object)
- Rust: fluent `.label("app", "engine").label("user.id", "alice")` chain
[VERIFIED from `docs.microsandbox.dev/sandboxes/labels.md`]

### Selectors

**YES — `--label` as AND-match selector on fleet commands.**
Multiple `--label` flags are AND-matched; a sandbox must carry all specified labels
to be selected.

```bash
msb ps --label app=engine                     # list matching sandboxes
msb ps --label app=engine --label tier=web    # require BOTH labels
```

`--label` as selector works on: `ps`, `ls`, `start`, `stop`, `restart`, `ping`,
`touch`, `rm`. No `--filter label=k=v` Docker-style syntax; it IS `--label` directly
on the fleet verb.
[VERIFIED from `docs.microsandbox.dev/sandboxes/labels.md` — "Select in bulk" section]

**SDK selector:**
```python
page = await Sandbox.list_with(labels={"app": "engine"})
```
```go
page, err := m.ListSandboxesWith(ctx,
    m.WithListLabels(map[string]string{"app": "engine"}),
)
```
[VERIFIED from `docs.microsandbox.dev/sdk/python.md`]

### Batch Operations

**LIFECYCLE BATCH: YES. EXEC BATCH: NO — EXEC IS SINGLE-SANDBOX ONLY.**

`--label` drives fleet lifecycle commands (stop, start, restart, rm) in bulk:
```bash
msb stop --label app=engine        # stop every matching sandbox
msb restart --label app=engine     # restart every match
msb rm --force --label app=engine  # remove every match
```

**`exec` is NOT in this list.** There is no `msb exec --label` or equivalent.
Exec targets a single named sandbox. The MCP `sandbox_exec` tool also takes a
single sandbox name.

This is a load-bearing finding: the reference project with the richest label/selector
system deliberately excluded `exec` from batch operations. Batch exec is not a
primitive in any of the three references.
[VERIFIED from `docs.microsandbox.dev/sandboxes/labels.md` — "Select in bulk" section, which enumerates `ps`, `ls`, `start`, `stop`, `restart`, `ping`, `touch`, `rm` — no `exec`]

### Exec (single sandbox)

CLI:
```bash
msb exec <name> -- <argv>
msb shell <name>               # shell command string variant
```

MCP tools (single sandbox):
- `sandbox_exec` — argv command with env, cwd, user, TTY, timeout, rlimits, stdin, output caps
- `sandbox_shell` — shell command string with same controls
- `sandbox_exec_start` — start long-running command, returns exec session ID
- `sandbox_exec_poll` — poll output events and exit status for exec session
- `sandbox_exec_write_stdin` — write to exec session stdin
- `sandbox_exec_signal` — send signal (`hup`, `int`, `term`, `kill`, numeric)
- `sandbox_exec_close` — close and forget exec session

Also: `sandbox_run` — ephemeral create+exec+remove in one step (no persistent sandbox).
[VERIFIED from `github.com/superradcompany/microsandbox-mcp` README]

### Lifecycle (full verb list)

```
msb create <image> --name <n> [--label KEY=VALUE ...]
msb start [<name>] [--label KEY=VALUE ...]
msb stop [<name>] [--label KEY=VALUE ...]
msb restart [<name>] [--label KEY=VALUE ...]
msb rm [--force] [<name>] [--label KEY=VALUE ...]
msb ps / msb ls [--label KEY=VALUE ...]       # list
msb status <name>
msb inspect <name>
msb modify <name> [--label KEY=VALUE] [--label-rm KEY]
msb exec <name> -- <cmd>
msb shell <name>
msb drain <name>
msb ping <name>       # local only
msb touch <name>      # local only
```

Also: ephemeral `sandbox_run` (MCP).
[VERIFIED from `docs.microsandbox.dev/sandboxes/labels.md` + MCP README]

### Artifact / Preview Handling

**NONE documented.** No bundle extraction, artifact download, or harvest command
exists in the microsandbox CLI, SDK, or MCP surface.
[VERIFIED from full docs index at `docs.microsandbox.dev/llms.txt`]

### Response Envelope (MCP/API)

**YES — documented shape:**
```json
{ "ok": true, "data": ... }           // success
{ "ok": false, "error": ... }         // failure
```

Large command output, file reads, and log output are capped and include truncation
metadata when shortened. Exact truncation field names not spelled out in the MCP
README prose ("truncation metadata when shortened") — the precise field names
(`bytes_omitted`, `total_bytes` or similar) are UNKNOWN from indexed docs.
[VERIFIED for `ok`/`data`/`error` shape from `github.com/superradcompany/microsandbox-mcp` README.
UNKNOWN for exact truncation field spellings.]

---

## Cross-Project Comparison Table

| Capability | clawk | OpenShell | microsandbox |
|---|---|---|---|
| **Labels — flag spelling** | NONE (VERIFIED) | NONE (VERIFIED) | `--label KEY=VALUE` repeatable (VERIFIED) |
| **Labels — domain model** | N/A | N/A | `map[string]string` on sandbox object (VERIFIED) |
| **Labels — mutable post-create** | N/A | N/A | Yes: `msb modify --label`/`--label-rm` (VERIFIED) |
| **Selectors — flag spelling** | NONE (VERIFIED) | NONE (VERIFIED) | `--label KEY=VALUE` AND-matched directly on fleet verbs (VERIFIED) |
| **Selector semantics** | N/A | N/A | AND-match across multiple `--label` flags (VERIFIED) |
| **Batch lifecycle (stop/start/rm by label)** | NONE (VERIFIED) | NONE (VERIFIED) | YES: `msb stop/restart/rm --label` (VERIFIED) |
| **Batch exec (exec across N sandboxes)** | NONE (VERIFIED) | NONE (VERIFIED) | **NO** — exec is single-sandbox only (VERIFIED) |
| **Exec verb — exact form** | `debug vshell [<name>] [-- cmd]` (escape hatch) (VERIFIED) | `sandbox connect [name]` (SSH only) (VERIFIED) | `msb exec <name> -- cmd` / `msb shell <name>` (VERIFIED) |
| **Lifecycle — start** | `clawk up` (VERIFIED) | `openshell sandbox create` (VERIFIED) | `msb start` (VERIFIED) |
| **Lifecycle — stop** | `clawk down` (VERIFIED) | UNKNOWN | `msb stop` (VERIFIED) |
| **Lifecycle — destroy** | `clawk destroy` (VERIFIED) | UNKNOWN | `msb rm` (VERIFIED) |
| **Lifecycle — list** | `clawk list` (VERIFIED) | `openshell sandbox list` (VERIFIED) | `msb ps` / `msb ls` (VERIFIED) |
| **Lifecycle — pause/resume** | `clawk pause` / `clawk resume` (VERIFIED) | UNKNOWN | UNKNOWN |
| **Lifecycle — snapshot** | `clawk snapshot` (VERIFIED) | UNKNOWN | UNKNOWN |
| **Artifact / bundle extraction** | `clawk pr` (workflow verb, ticket mode only) | NONE (VERIFIED) | NONE (VERIFIED) |
| **JSON flag** | `--json` on read commands (VERIFIED) | UNKNOWN | UNKNOWN for CLI; MCP has envelope (VERIFIED) |
| **MCP/API response envelope** | No MCP surface | UNKNOWN | `{ok, data\|error}` + truncation metadata (VERIFIED) |
| **Truncation fields (exact)** | N/A | UNKNOWN | UNKNOWN (prose only: "truncation metadata") |

---

## Gaps

1. **clawk labels**: Confirmed absent from all documented CLI flags. Possible the
   Go source implements something undocumented — not checked. Confidence in NONE is HIGH
   because the commands.md grep returned zero label-related lines.

2. **OpenShell destroy/stop/remove**: Almost certainly exists but verb spelling not found
   in any accessible page. The docs redirect CLI reference to `docs.nvidia.com/openshell/latest/reference/cli`
   which returned 404 during this research.

3. **OpenShell exec**: `sandbox connect` is SSH-based interactive. No non-interactive
   `exec -- cmd` verb found. May exist under a different name or only via MCP.

4. **microsandbox truncation field names**: The MCP README says large output "includes
   truncation metadata when shortened" but does not spell out `bytes_omitted`/`total_bytes`.
   The exact field names could be confirmed by reading the MCP server source at
   `github.com/superradcompany/microsandbox-mcp`.

5. **clawk --label (source-level)**: The clawk README and docs/commands.md contain no
   label flag. The project is early-stage; a label system may be planned but is not
   shipped in any documented form as of 2026-08-15.

---

## Recommended Next Step

Read `github.com/superradcompany/microsandbox-mcp/src/tools/sandbox_exec.ts` (or
equivalent) to confirm exact truncation field names — that is the last UNKNOWN with
practical impact on nexus3's MCP envelope design.

---

## Decision Notes for nexus3

**Q1 — Labels as `map[string]string` vs. single-purpose ID field:**
microsandbox is the only reference with labels; it stores them as `map[string]string`.
The Python SDK uses `dict`, Go uses `map[string]string`, TypeScript uses object literal.
There is no "internal single-purpose ID field with `--label` as sugar" pattern anywhere
in the reference surface. The evidence supports a plain `map[string]string` on the
domain object, with `--label KEY=VALUE` as the repeatable CLI flag for both setting
and selecting.

**Q2 — Batch exec across N sandboxes:**
Zero of three reference projects ship batch exec. microsandbox — the richest reference
with a full label+selector system — explicitly excludes `exec` from the fleet commands
that accept `--label`. The verb list for `--label` fleet ops is: `ps`, `ls`, `start`,
`stop`, `restart`, `ping`, `touch`, `rm`. Exec is not in it. nexus3's proposed
`exec --label` would exceed the reference surface. The evidence is strong: retract
batch exec to a wrapper layer, or if kept, document it as a deliberate nexus3
extension beyond what any reference provides.
