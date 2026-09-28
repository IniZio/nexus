---
id: CRED-R-002
concept: C-CRED
summary: "Controller-launched guests run in bypassPermissions mode with IS_SANDBOX=1. Delegate guests (delegate_agent_dispatch) run in auto mode. The sandboxed egress policy (default-deny passt; only allowlisted and brokered hosts reachable) is the security boundary."
criticality: must
verification: automated
status: active
trace: AC-2
---

**Controller path** (`nexus-controller`): guest Claude **shall** run with `--permission-mode bypassPermissions`, `IS_SANDBOX=1` in the pane environment, and `skipDangerousModePermissionPrompt = true` in the flagSettings file. The sandboxed egress policy (default-deny passt; only allowlisted and explicitly brokered hosts reachable) is the real security boundary. Permission-mode confirmation dialogs are suppressed so controller agents can complete briefs unattended.

**Delegate path** (`delegate_agent_dispatch` / `nexus herdr agent`): guest Claude **shall** run in `auto` permission mode (derived from the host `~/.claude/settings.json` overlay). The `claudeReadyMatch` detector **shall** match the auto-mode ready footer (`"auto mode on"`) and **shall not** match the bypass-mode footer. A delegated brief **shall** complete unattended without any bypass flag.

- **Why bypassPermissions for controller** — `auto` mode blocks unattended tool execution behind confirmation dialogs, stalling controller-spawned agents. The egress gate (not the permission mode) enforces the network boundary.
- **Fit criterion (controller)** — `--permission-mode bypassPermissions` in agent start argv; `IS_SANDBOX=1` in pane env before start; `skipDangerousModePermissionPrompt:true` in settings JSON.
- **Fit criterion (delegate)** — `--permission-mode auto` in delegate argv; `claudeReadyMatch` matches `"auto mode on"`.
- **Verification** automated · **Criticality** must · **Source** nexus-mount-creds-ssh-relay#AC-2
- **Tests** `TestDefaultPermModeIsBypassPermissions`, `TestProvisionBypassSetsIsSandbox`, `TestRestartBypassSetsIsSandbox`, `TestControllerSettingsJSONBypassIncludesSkipPrompt`, `TestPaneRunFallbackBypassHasIsSandbox` (`internal/controller/backend/herdr/backend_test.go`); `TestClaudeReadyMatch_AutoModeFooter`, `TestClaudeReadyMatch_MatchesAutoTranscript`, `TestClaudeReadyMatch_DoesNotMatchBypassFooter` (`internal/cli/claude_ready_match_test.go`)
