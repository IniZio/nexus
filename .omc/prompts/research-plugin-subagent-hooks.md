# Research: OpenCode Plugin Hooks with Subagents

## Question
When a subagent executes a tool, does the main agent's plugin hook fire?

Specifically:
1. Does each subagent have its own plugin instance?
2. Do tool calls in subagents trigger the main agent's "tool.execute.before" hook?
3. What is the relationship between parent session and subagent sessions in plugin context?

## Context
The user has a "boulder" plugin that uses `tool.execute.before` hook. They want to know if this hook fires when a subagent (like @general) executes tools.

## Relevant OpenCode Docs Found

### Plugin Events (from plugins.md)
- `tool.execute.before` - fires before tool execution
- `tool.execute.after` - fires after tool execution

### Session Events (from plugins.md)
- `session.created`
- `session.compacted`
- `session.deleted`
- `session.diff`
- `session.error`
- `session.idle`
- `session.status`
- `session.updated`

### Agent Types (from agents.md)
- Primary agents: Main assistants (Build, Plan)
- Subagents: Specialized assistants invoked by @mention or by primary agents
- Subagents create "child sessions"

### Session Navigation (from agents.md)
- "When subagents create their own child sessions, you can navigate between the parent session and all child sessions"

## What to Research

1. **Plugin Context in Subagents**: Look for any documentation or code about whether plugins are loaded per-session or globally, and how this affects subagents.

2. **Session Hierarchy**: Understand if child sessions (created by subagents) share the same plugin context as the parent session, or if they have isolated plugin contexts.

3. **Tool Hook Execution Path**: Determine if `tool.execute.before` fires at the global level, parent session level, or subagent session level.

4. **Real-world Examples**: Look for any plugins that handle subagents differently, or any issues/discussions about this.

## Deliverables
Provide a clear answer about whether the main agent's `tool.execute.before` hook fires when a subagent executes tools, with explanation of the session/plugin hierarchy.
