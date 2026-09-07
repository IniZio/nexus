---
max_turns: 10
runs: 1
allowed_tools: [Read, Glob, Grep, Skill]
tags: [activation, egress]
---

I'm setting up a nexus3 sandbox for a project that needs GitHub access. How do I make
sure my real GH_TOKEN never gets sent to other repos or untrusted hosts? Can the
sandbox call GitHub's GraphQL API, and what happens if it tries to hit a different
org's REST endpoint?
