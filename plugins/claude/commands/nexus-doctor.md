# /nexus:nexus-doctor

Report the real state of the nexus substrate, the MCP connection, and running
sandboxes. Run this before delegation to confirm everything is reachable.

## What this command does

Runs the commands below in order and reports their output. Do not invent
diagnostics that have no backing command — each item names its source.

### 1. Binary version

```bash
~/.local/bin/nexus version
```

Reports the installed version and build commit. If the command fails, the binary
is missing from `~/.local/bin/`.

### 2. Substrate capability

```bash
~/.local/bin/nexus doctor
```

Checks KVM, cloud-hypervisor binary, kernel image, and virtiofsd. Every line
must show `[OK  ]`; any `[FAIL]` blocks sandbox creation.

### 3. herdr availability

```bash
~/.local/bin/nexus herdr doctor
```

Reports the herdr binary path, plugin ABI version, and whether `HERDR_VERSION`
is set. If herdr is missing or the ABI does not match, delegation via herdr
subcommands will fail.

### 4. MCP connectivity

Check whether the nexus MCP server is connected by calling the `sandbox_list`
tool. If the tool call succeeds, the server is live. If it errors, the MCP
server process is not running — restart Claude Code to reconnect. Note the
binary the MCP server is registered against:

```bash
grep -A3 '"nexus"' ~/.claude.json
```

If that binary is not `~/.local/bin/nexus`, the server may be running a stale
build that lacks the delegation tools (`delegate_worktree_create`,
`delegate_agent_dispatch`, `delegate_agent_poll`, `delegate_teardown`).

### 5. Running sandboxes and RAM cost

```bash
~/.local/bin/nexus ps --json
```

List every running sandbox. Each running sandbox holds memfd-backed guest RAM
that is resident and unswappable. The default allocation is 512 MiB per sandbox;
check the `memory_mib` field if non-default sizes were requested. Report the
running count and total estimated RAM in use.

### 6. herdr workspaces

```bash
herdr workspace list
```

Report the workspace count and flag any with `is_linked_worktree: true` — those
are active worktree sandboxes. A worktree workspace with no matching entry in
`nexus ps` output is an orphan (herdr binding with no live VM).

## Invocation

```
/nexus:nexus-doctor
```

No arguments. Runs against the host environment; does not require a repo.
