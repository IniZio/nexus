# /nexus:nexus-init

First-run setup for a repo that has never used nexus.

## What this command does

Loads the `nexus:nexus` skill, opens `references/onboard.md`, and walks through it against the current repo:

1. Detects VCS provider, host, and repo identity from `git remote get-url origin`.
2. Scans all Dockerfiles and compose files to derive the build-time egress host list.
3. Authors `.nexus/config.yaml` inside the `.nexus/` directory at the repo root.
4. Authors `.nexus/Containerfile` if one does not already exist.
5. Validates the generated config with `nexus config validate` (prints the resolved path and an effective-config summary, or the load error — see the onboarding reference, Step 5).
6. Explains where egress config is read from: the checkout's `.nexus/config.yaml`, effective on the next worktree-sandbox create.

## When to use it

Run `/nexus:nexus-init` once per repo, before the first `nexus herdr workspace-create` or `nexus create`. If `.nexus/config.yaml` already exists, read it directly instead.

## Invocation

```
/nexus:nexus-init
```

No arguments. The command operates on the repo containing the current working directory.

---

Load skill: nexus:nexus — then open `references/onboard.md`.
