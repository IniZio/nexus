# nexus3 Claude Code plugin

Registers the `nexus3` skill (sandbox operations, first-run onboarding, egress
policy, delegating work into a worktree sandbox), the `/nexus3:nexus3-init`,
`/nexus3:nexus3-delegate` and `/nexus3:nexus3-doctor` commands, and the
`nexus3 mcp` stdio server for use in any Claude Code session.

## Requirements

`nexus3` must be on `PATH` — the MCP server is launched as `nexus3 mcp`. Install
it via the herdr plugin (see the
[quickstart](../../docs/site/quickstart.md#install)), or build from source with
`go build -o ~/.local/bin/nexus3 ./cmd/nexus3`.

## Install

```sh
claude plugin marketplace add IniZio/nexus3
claude plugin install nexus3@nexus3
```

Run `/nexus3:nexus3-doctor` in a Claude Code session to confirm the MCP server
connects.

## Uninstall

```sh
claude plugin uninstall nexus3@nexus3
claude plugin marketplace remove nexus3
```

## Developing the plugin

From a checkout of this repo:

```sh
make install-plugin
```

This registers the checkout as a local marketplace and installs the plugin from
it, so edits under `plugins/claude/` are picked up on the next
`claude plugin update nexus3@nexus3`.

Evals live in `evals/`; run them with `claude plugin eval plugins/claude`.
