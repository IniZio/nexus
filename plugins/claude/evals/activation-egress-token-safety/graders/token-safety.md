---
type: llm
weight: 1
---

A correct response must cover all three points:

1. The guest never receives the real GH_TOKEN — it holds a 64-hex placeholder. The
   host supervisor intercepts outbound requests and substitutes the real token only
   for the configured secret host.

2. Cross-repo or cross-org REST calls are refused (403) because path policies restrict
   which endpoints the substituted token reaches.

3. GraphQL is blocked from the sandbox (gh pr create fails; use REST instead).

Fail if the response suggests the real token enters the guest, or if it claims
the sandbox can make unconstrained GitHub API calls, or if it omits the
placeholder/interception mechanism entirely.
