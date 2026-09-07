---
type: llm
weight: 1
---

A correct response must:

- Direct the user to use the GitHub REST API directly (e.g. `gh api -X POST
  /repos/{owner}/{repo}/pulls`) rather than `gh pr create`.
- Explain that `gh pr create` is blocked inside sandboxes because it uses
  the GraphQL API.

Fail if the response recommends `gh pr create` without qualification, or if it
does not mention the REST API alternative.
