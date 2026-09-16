---
title: "Configuration"
description: "Self-contained image build with --context and --dockerfile"
---

# Configuration

> Build a guest image from a Dockerfile at create time, without a separate build step.

The `--context <dir>` flag on `create` enables a self-contained build mode: nexus3 starts a buildkitd inside the VM, builds the image from the supplied Dockerfile context, and then boots the sandbox from the resulting image. No pre-built image reference is needed.

::: warning Implementation spelling <Badge type="warning" text="partial" />
The flag is built today as `--file`. The target spelling is `--context`. All examples on this page use the target spelling.
:::

## Self-contained build flags

These flags are part of `create`. See [Lifecycle commands](/cli/sandbox-commands) for the full flag set.

| Flag | Type | Default | Description |
|---|---|---|---|
| `--context <dir>` | string | — | Dockerfile build context directory on the host |
| `--dockerfile <path>` | string | `Dockerfile` | Dockerfile path, relative to `--context` |

`--context` and `--image` / `--rootfs` are mutually exclusive: exactly one image source must be provided.

## Example

```
nexus3 create myproject/dev-1 \
  --context /data/repos/myrepo \
  --memory 8192
```

<Badge type="warning" text="partial" /> — current implementation uses `nexus3 sandbox create` and `--file`; see [CLI sandbox commands](/cli/sandbox-commands) for the mapping.

nexus3 copies the context directory into the VM, runs `buildkitd`, builds the image, and boots the sandbox. The build cache is stored on a virtio-blk disk and reused across subsequent `--context` creates for the same project.

## What `--context` captures

The context directory is captured at create time. The capture includes:

- Tracked files (including dirty/modified tracked files)
- Untracked files
- Unpushed commits

It does not include `.git` history beyond what the working tree reflects.

## Project config file (`.nexus/config.yaml`)

`.nexus/config.yaml` is an optional per-repository configuration file placed inside the `.nexus/` directory at the repository root. nexus3 discovers it by walking up from the process working directory to the nearest directory that contains a `.git` entry (the repository root). An absent file is a no-op. A present but malformed file, or a file with an unknown YAML key, is a hard error.

```yaml
version: 1

# Extend the outbound allowlist for all sandboxes in this repo.
egress:
  allow:
    - proxy.golang.org
    - sum.golang.org
    - storage.googleapis.com

  # Path policies: destination-scoped allowlists (host-keyed).
  policy:
    # GitHub: policy entries define the allowed paths for this repo.
    - host: github.com
      paths: ["/owner/name/**"]
    - host: api.github.com
      paths: ["/repos/owner/name/**", "/repos/owner/name", "/user"]
    - host: uploads.github.com
      paths: ["/**"]

    # Generic per-host path allowlist (any provider).
    - host: api.example.com
      paths: ["/v4/projects/123/**"]

  # SECURITY WARNING — GitHub glob tightness is the author's responsibility.
  # The policy layer is generic default-deny globs; the system cannot automatically
  # narrow what you write. For GitHub hosts you MUST:
  #   • Scope every api.github.com path to /repos/<owner>/<repo>/** (plus specific
  #     endpoints like /repos/<owner>/<repo> and /user). Do NOT write /** or / at root.
  #   • Do NOT list /graphql under api.github.com. GraphQL is a parallel write channel
  #     that bypasses path-allowlist semantics; listing it reopens the sole-bound risk
  #     (the operator's full-scope token becomes the only protection).
  #   • Do NOT list /** under github.com.
  # An unscoped or /graphql-listing GitHub glob reopens the sole-bound risk.
  # A stricter parse-time graphql lint (automatic floor) is a future TBD option.

  # Brokered VCS/API secrets — injected as 64-hex placeholders in the guest;
  # real token swapped host-side by the MITM proxy (PDF-R-020).
  secrets:
    # GitHub
    - env: GH_TOKEN
      hosts: [github.com, api.github.com, uploads.github.com]

    # Non-GitHub
    - env: GITLAB_TOKEN
      hosts: [gitlab.com]

    # Generic API token
    - env: API_TOKEN
      hosts: [api.example.com]

# Default sandbox settings for this repo.
sandbox:
  image: sha256:<digest>     # default --image; overridden by --image flag
  memory: 4096               # MiB; overridden by --memory flag
  vcpus: 2                   # overridden by --vcpus flag
  agent: claude-code         # default agent profile; overridden by --agent flag

  mounts:
    - ./src:/work/src        # relative paths resolved from the repository root (the directory holding .nexus/)
```

Flag precedence: explicit CLI flags win over `.nexus/config.yaml` values; `.nexus/config.yaml` values win over built-in defaults.

`egress.allow` is **additive** — config hosts are unioned with `--allow-host` flags; neither replaces the other.

A host is **either open or policy-gated, never both.** A host listed under `egress.allow` is open passthrough (any path, no credential). A host listed under `egress.policy` or `egress.secrets[].hosts` is policy-gated: default-deny on paths, credential brokered. Because the policy layer takes precedence, an `allow` entry for a policy-gated host would be silently inert — the file would claim open access the perimeter does not grant. `config.Load` therefore rejects the file at parse time (hostnames compare case-insensitively):

```text
nexus3 config: host "github.com" is listed under egress.allow and egress.policy; a host can be open (allow) or policy-gated (policy/secrets), not both — remove it from egress.allow
```

For policy-gated `github.com`, public archive and release downloads (`/<owner>/<repo>/archive/...`, `/<owner>/<repo>/releases/download/...`) are already permitted without an `allow` entry; see [Egress and perimeter](../security/egress-and-perimeter.md#github-and-the-request-allowlist).

`sandbox.mounts` is **replaced** by any explicit `--mount` flag on the command line. To use both, list all mounts in `.nexus/config.yaml` and omit `--mount` on the command line.

### Config source for worktree sandboxes

Worktree sandboxes (auto-created by the herdr plugin) read `.nexus/config.yaml` from the worktree's own checkout — the same file the `--file` build reads. Egress policy, brokered secrets, and `sandbox.nested` all come from that file. A change takes effect on the next worktree-sandbox create for that checkout; no push to the default branch is needed. A checkout without the file gets no egress policy and no nested opt-in; a malformed file is an error. See the [agent skill](https://github.com/IniZio/nexus3/blob/main/plugins/claude/skills/nexus3/SKILL.md) for the authoring workflow.
