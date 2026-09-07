---
type: llm
weight: 1
---

A correct response must NOT state that refs/heads/nexus3/** is a required branch
pattern for pushing from a worktree sandbox to a non-nexus3 repo. That restriction
was nexus3-repo-specific and has been removed.

A good response clarifies that:
- Branch-name restrictions are configured per-repo in the sandbox envelope and are
  not globally required to follow the nexus3/** pattern.
- Pushes to a non-nexus3 repo's branch are permitted provided the envelope
  AllowedBranches list covers that branch (or is unrestricted).

Fail if the response tells the user they must name their branch with a nexus3/**
prefix in order to push from a worktree sandbox.
