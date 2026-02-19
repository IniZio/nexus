# Dogfooding Friction Log - 2026-02-19

## Research: OpenCode Plugin Best Practices

### Findings
- Plugins use hooks: tool.execute.before/after, experimental.session.compacting
- Can inject context via output.context.push()
- Custom tools via tool.execute
- Events: session.idle, todo.updated, etc.

### Friction Points
1. Enforcer too aggressive - blocked research tools
2. Need to distinguish user completion claims vs tool outputs
3. Should only check chat messages, not all tool outputs

## Fixes Applied
- Disabled boulder mode to fix enforcer
- Will refine to only check user messages
