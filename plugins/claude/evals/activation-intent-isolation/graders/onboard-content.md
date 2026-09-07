---
type: llm
weight: 1
---

The response should guide the user through first-time sandbox onboarding for a
repo that uses Docker Compose and GitHub. A good response:

- Mentions authoring a nexus3.yaml (or equivalent sandbox config) that includes
  egress rules derived from the project's actual remotes and build manifests.
- Mentions a .nexus/Containerfile (or equivalent guest image customization file)
  as part of the setup.
- Does NOT claim a wholesale `/**` path is acceptable in the egress policy (that
  is an unsafe catch-all).
- Does NOT suggest mounting ~/.claude directly into the sandbox (that would leak
  the real token).

Fail if the response has no concrete first steps and only gives generic advice
unrelated to isolated-VM sandbox onboarding.
