---
id: CRED-R-002
concept: C-CRED
summary: "Guest Claude runs in auto permission mode: no process in the guest carries --dangerously-skip-permissions, no seeded file sets bypassPermissions or skipDangerousModePermissionPrompt, and the agent completes a delegated brief unattended via delegate_agent_dispatch."
criticality: must
verification: automated
status: active
trace: AC-2
---

In every claude-code sandbox, no process **shall** carry `--dangerously-skip-permissions` and no seeded file **shall** set `bypassPermissions` or `skipDangerousModePermissionPrompt`. The guest agent **shall** run in Claude Code `auto` permission mode (derived from the host `~/.claude/settings.json` which is live-mounted). The `claudeReadyMatch` detector **shall** match the auto-mode ready footer (`"auto mode on"`) and **shall not** match the bypass-mode footer.

The agent **shall** complete a delegated brief unattended via `delegate_agent_dispatch` / `nexus herdr agent` without any bypass flag.

- **Why** — `--dangerously-skip-permissions` grants the agent unconstrained tool use without any confirmation surface. Auto mode retains Claude Code's built-in safety confirmation layer while still allowing unattended operation within the configured rules.
- **Fit criterion** — `grep -rn 'dangerously-skip-permissions\|bypassPermissions\|skipDangerousModePermissionPrompt' --include=*.go` (excluding test assertions of absence) returns 0 hits; in guest `grep -c dangerously /proc/$(pgrep -n claude)/cmdline` = 0; a delegated brief completes without human intervention.
- **Verification** automated · **Criticality** must · **Source** nexus-mount-creds-ssh-relay#AC-2
- **Tests** `TestClaudeReadyMatch_AutoModeFooter` (`internal/cli/claude_ready_match_test.go:19`); `TestClaudeReadyMatch_MatchesAutoTranscript` (`claude_ready_match_test.go:33`); `TestClaudeReadyMatch_DoesNotMatchBypassFooter` (`claude_ready_match_test.go:42`)
