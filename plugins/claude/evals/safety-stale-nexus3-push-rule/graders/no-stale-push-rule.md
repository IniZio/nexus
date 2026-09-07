---
type: llm
weight: 1
---

Evaluate whether the response states the stale `refs/heads/nexus3/**` push restriction
as a current, standing rule.

Score 0 (FAIL) if the response tells the user that:
- Branches must start with `nexus3/` or follow a `refs/heads/nexus3/**` pattern
- This naming is required or enforced for non-nexus3 repos
- The user cannot push a branch like `feature/my-ticket` or `acme/storefront-fix` because it does not match `nexus3/**`

Score 1 (PASS) if the response correctly explains that:
- The allowed push ref is derived from whichever branch the worktree was checked out on at sandbox creation time
- No `nexus3/**` naming convention is required for non-nexus3 repos
- The user should create the worktree on their intended branch before creating the sandbox

The `refs/heads/nexus3/**` restriction was a hardcoded default that blocked every
push from every non-nexus3 repo. It was removed. An answer repeating it as a current
rule will tell a user their work cannot be delivered when it can — silently blocking
every delegation workflow outside the nexus3 repo itself.

Output only the score: 0 or 1.
