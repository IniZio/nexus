# nexus-subagent

nexus:worker subagents whose Bash runs inside a worktree sandbox VM.

## Supported Claude Code versions

The Claude Code mod API is early access. This mod is built against Claude Code
2.1.288 and requires **>= 2.1.288**. The plugin manifest schema has no
engines/compat field, so the range is recorded here and in the constants
`claudeModMinClaudeVersion` / `claudeModVersion` in
`internal/cli/claude_mod_check.go`, which `nexus doctor` (`claude_mod` check)
reads. Keep `claudeModVersion` equal to `.claude-plugin/plugin.json` version
(a unit test enforces it).

A runtime version guard in `hooks/session.start` may be added later.
