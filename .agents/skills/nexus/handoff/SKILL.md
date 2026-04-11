---

## name: handoff
description: This skill should be used to create and start a Nexus workspace and hand off a focused implementation prompt to an agent in that workspace.

# Handoff

## Workflow

1. Run `scripts/create_workspace_handoff.sh`.
2. Confirm workspace ID and worktree path.
3. Confirm prompt file created.
4. Execute supplied handoff command or print manual command.

## Command

```bash
bash skills/nexus/handoff/scripts/create_workspace_handoff.sh \
  --repo "<repo-url-or-path>" \
  --workspace-name "<workspace-name>" \
  --objective "<implementation-objective>" \
  --backend "<optional: firecracker|seatbelt>" \
  --handoff-command 'cd "__WORKTREE__" && opencode "$(cat "__PROMPT_FILE__")"'
```

