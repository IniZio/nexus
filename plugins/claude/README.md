# nexus Claude Code plugin

Registers the `nexus` skill (sandbox operations, first-run onboarding, egress
policy, delegating work into a worktree sandbox), the `/nexus:nexus-init`,
`/nexus:nexus-delegate` and `/nexus:nexus-doctor` commands, and the
`nexus mcp` stdio server for use in any Claude Code session.

## Requirements

`nexus` must be on `PATH` — the MCP server is launched as `nexus mcp`. Install
it via the herdr plugin (see the
[quickstart](../../docs/site/quickstart.md#install)), or build from source with
`go build -o ~/.local/bin/nexus ./cmd/nexus`.

## Install

```sh
claude plugin marketplace add IniZio/nexus
claude plugin install nexus@nexus
```

Run `/nexus:nexus-doctor` in a Claude Code session to confirm the MCP server
connects.

## Uninstall

```sh
claude plugin uninstall nexus@nexus
claude plugin marketplace remove nexus
```

## Developing the plugin

From a checkout of this repo:

```sh
make install-plugin
```

This registers the checkout as a local marketplace and installs the plugin from
it, so edits under `plugins/claude/` are picked up on the next
`claude plugin update nexus@nexus`.

Evals live in `evals/`; run them with `claude plugin eval plugins/claude`.
