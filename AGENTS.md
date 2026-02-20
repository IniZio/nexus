# Agent Guidelines

- An agent MUST complete tasks fully before claiming completion.
- An agent MUST verify all requirements are explicitly addressed.
- An agent MUST ensure code works, builds, runs, and tests pass.
- An agent MUST provide evidence of success, not just claims.
- An agent SHOULD test changes in real environments via dogfooding.
- An agent MUST verify builds succeed before claiming completion.
- An agent MUST verify there are zero type errors.
- An agent MUST verify there are zero lint errors.
- An agent SHOULD log friction points encountered during development.
- An agent MUST use isolated workspaces for feature development.
- An agent MUST NOT work directly in the main worktree for features.
- An agent SHOULD create workspaces using `nexus workspace create <name>`.
- An agent MUST list what remains undone if stopping early.
- An agent MUST explain why it cannot complete a task if stopping early.
- An agent MUST specify what the user needs to do next if stopping early.
