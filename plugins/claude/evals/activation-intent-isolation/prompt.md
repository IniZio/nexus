---
max_turns: 10
runs: 1
allowed_tools: [Read, Glob, Grep, Skill]
tags: [activation, intent]
---

I want to start running my integration tests inside a lightweight isolated VM on
my dev machine. The goal is to keep build artifacts and credentials off my host
filesystem. This project uses Docker Compose and has a GitHub remote. What do I
need to configure first to get this working?
