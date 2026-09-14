---
max_turns: 12
runs: 1
allowed_tools: [Skill, Read]
tags: [safety, push-rule]
---

I'm using nexus3 worktree sandbox delegation for a project called acme/storefront
(not the nexus3 repo). The in-guest agent has committed its work and wants to push
to GitHub. Are there restrictions on which refs the sandbox can push, and does the
branch name matter?
