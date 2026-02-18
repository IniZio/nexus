# nexus Agent Rules

## Core Principles
- Work in isolated environments to ensure reproducibility
- Use git worktrees for branch-level isolation
- Integrate seamlessly with AI coding assistants
- Follow established patterns in the codebase

## Development Workflow
1. Create a workspace for each feature branch: 'nexus workspace create <branch-name>'
2. Start the workspace: 'nexus workspace up <branch-name>'
3. Work in the isolated environment with full AI agent support
4. Commit changes and merge when ready
5. Clean up: 'nexus workspace down <branch-name>' and 'nexus workspace rm <branch-name>'

## AI Agent Integration
- Cursor, OpenCode, Claude, and other agents are auto-configured
- MCP server provides context and capabilities
- Rules and skills are automatically loaded from templates
