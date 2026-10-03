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

## sandbox_* tools

`sandbox_create`, `sandbox_start`, `sandbox_stop`, `sandbox_exec`, `sandbox_remove`, `sandbox_run` and
`sandbox_list` mirror the names and arguments of `nexus mcp`. Each call execs
the `nexus` binary on PATH, so a newly installed binary applies on the next
call with no reconnect. The tools are registered at `session.start`; restart
the session to see them. Not provided: pause/resume (no CLI verb).
`sandbox_run` needs a ref or digest and does not support `rootfs_path` or
`nested_virt`. Expect about 1 s of exec latency per call.
