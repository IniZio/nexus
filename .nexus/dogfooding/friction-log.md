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
# Boulder Tasks Completed - Thu Feb 19 07:27:54 AM UTC 2026

## 1. Error Handling
- Wrapped all async operations in try-catch
- Added defaults for config values
- Graceful degradation implemented

## 2. Performance Refactoring
- Minimal synthetic messages
- Efficient regex matching
- Hot reload config

## 3. Research: OpenCode Plugins
- Documented hooks and events
- Learned toast notification API
- Understood synthetic part injection
