# /nexus3:nexus3-init

First-run setup for a repo that has never used nexus3.

## What this command does

Loads the `nexus3:nexus3-onboard` skill and walks through it against the current repo:

1. Detects VCS provider, host, and repo identity from `git remote get-url origin`.
2. Scans all Dockerfiles and compose files to derive the build-time egress host list.
3. Authors `nexus3.yaml` at the repo root.
4. Authors `.nexus/Containerfile` if one does not already exist.
5. Validates the generated config with `~/.local/bin/nexus3 config show --repo-dir .`.
6. Explains the trust-anchor ritual — what must be merged and why existing sandboxes are not updated automatically.

## When to use it

Run `/nexus3:nexus3-init` once per repo, before the first `nexus3 herdr workspace-create` or `nexus3 create`. If `nexus3.yaml` already exists, inspect it with `nexus3 config show --repo-dir .` instead.

## Invocation

```
/nexus3:nexus3-init
```

No arguments. The command operates on the repo containing the current working directory.

---

Load skill: nexus3:nexus3-onboard
