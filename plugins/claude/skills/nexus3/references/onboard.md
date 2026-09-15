# First-run onboarding — author .nexus/config.yaml and .nexus/Containerfile

For a repo that has never used nexus3: detect the repo's stack, author
`.nexus/config.yaml` and `.nexus/Containerfile`, and explain the trust-anchor ritual.

Follow these steps in order. Each step has a concrete output. Do not skip to Step 4 before finishing Step 3.

---

## Step 1 — Detect VCS provider and repo identity

Run inside the repo checkout:

```sh
git remote get-url origin
```

Normalize the URL to `host` + `owner/name`:

| Remote URL form | Host | Owner/name |
|---|---|---|
| `https://github.com/acme/myrepo.git` | `github.com` | `acme/myrepo` |
| `git@github.com:acme/myrepo.git` | `github.com` | `acme/myrepo` |
| `https://gitlab.com/acme/myrepo.git` | `gitlab.com` | `acme/myrepo` |
| `git@gitlab.example.com:acme/myrepo.git` | `gitlab.example.com` | `acme/myrepo` |
| `https://bitbucket.org/acme/myrepo.git` | `bitbucket.org` | `acme/myrepo` |

Record: **VCS_HOST**, **OWNER**, **REPO**.

---

## Step 2 — Scan build manifests for egress hosts

Collect the set of external hosts the build actually fetches from. Work from evidence, not assumption.

### 2a. Collect all Dockerfiles and compose files

```sh
find . -maxdepth 6 \
  -not -path './.git/*' \
  -not -path './.claude/worktrees/*' \
  \( -name 'Dockerfile' -o -name 'Dockerfile.*' -o \
     -name 'docker-compose.yml' -o -name 'docker-compose.*.yml' -o \
     -name 'compose.yml' \) \
  | sort
```

Read each file. Collect every external host referenced by:

- `FROM <image>` — the registry the image is pulled from
- `COPY --from=<image>` — a BuildKit multi-stage source
- `image: <image>` in compose files — runtime service images
- `RUN apt-get`, `RUN apk add` — package repos implied by the base image OS
- `RUN uv sync` / `RUN pip install` — Python package index
- `RUN npm install` / `RUN pnpm install` — npm registry
- `RUN wget https://...` / `RUN curl -L https://...` — explicit archive downloads

### 2b. Resolve registry hosts per image reference

| Image reference pattern | Hosts to add |
|---|---|
| `image` (no registry prefix, e.g. `postgres:16`) | `registry-1.docker.io`, `auth.docker.io`, `production.cloudflare.docker.com` |
| `ghcr.io/...` | `ghcr.io` |
| `quay.io/...` | `quay.io` |
| `registry.example.com/...` | `registry.example.com` |
| `docker.io/library/...` | `registry-1.docker.io`, `auth.docker.io`, `production.cloudflare.docker.com` |

Docker Hub (`registry-1.docker.io`) requires **all three** hosts: auth, registry, and CDN. Omitting `production.cloudflare.docker.com` causes partial failures on blob pulls.

### 2c. Resolve package-manager and tool hosts

| What you see | Hosts to add |
|---|---|
| Debian/Ubuntu base image + `apt-get` | `deb.debian.org`, `security.debian.org` |
| Alpine base image + `apk add` | `dl-cdn.alpinelinux.org` |
| `uv sync` / `pip install` | `pypi.org`, `files.pythonhosted.org` |
| `npm install` | `registry.npmjs.org` |
| `pnpm install` | `registry.npmjs.org` |
| `RUN go get` / `go mod download` | `proxy.golang.org`, `sum.golang.org`, `storage.googleapis.com` |
| `RUN cargo build` | `static.crates.io`, `crates.io` |
| `wget https://github.com/...` (release archives) | `github.com`, `codeload.github.com` |
| `curl https://cli.github.com/...` | `cli.github.com` |

**`storage.googleapis.com` is not optional for Go.** `proxy.golang.org` redirects module zip downloads to signed GCS URLs; omitting it causes silent partial failures.

### 2d. Compose the `egress.allow` list

List only the hosts you found evidence for. No wildcards, no guesses.

**Never add a host you did not find evidence for in a Dockerfile, compose file, or manifest.**

---

## Step 3 — Author .nexus/config.yaml

Create `.nexus/config.yaml` inside the `.nexus/` directory at the repo root with `version: 1` as the first key.

### 3a. VCS egress block

#### GitHub

```yaml
version: 1
egress:
  policy:
    - host: api.github.com
      paths:
        - "/repos/OWNER/REPO"        # repo info
        - "/repos/OWNER/REPO/**"     # pulls, branches, commits, git/refs, contents, ...
        - "/user"                    # gh auth status
    - host: github.com
      paths:
        - "/OWNER/REPO/**"           # git clone/push over HTTPS
        - "/OWNER/REPO.git/**"       # git smart-HTTP
  secrets:
    - env: GH_TOKEN
      hosts: [github.com, api.github.com]
```

Replace `OWNER` and `REPO` with the values from Step 1.

**SECURITY — path scoping is your responsibility:**

- **Never write `/**` at root** for `api.github.com` or `github.com`. A path of `/**` lets a compromised guest reach every other repository with the operator's full-scope token. Scope every entry to `/repos/OWNER/REPO/...`.
- **Never list `/graphql`** under `api.github.com`. GraphQL is a parallel write channel that bypasses the path allowlist; listing it reopens the sole-bound risk documented in `nexus3-github-token-sole-bound`.
- Do not list `uploads.github.com` unless the workflow explicitly uploads release assets — it is not needed for clone, push, or REST API calls.

For egress policy semantics, verification probes, and the secret-brokering model, see `egress.md`.

#### GitLab (cloud or self-hosted)

```yaml
version: 1
egress:
  secrets:
    - env: GITLAB_TOKEN
      hosts: [VCS_HOST]
```

Non-GitHub VCS hosts have no mandatory path policy. Prefer a project-scoped access token over a full personal access token.

#### Other VCS hosts

Use the `egress.secrets` long form with `hosts: [VCS_HOST]`. Add a `policy:` entry with scoped paths only if you need to restrict which API paths the credential is forwarded to.

### 3b. Build-time egress block

Append the `egress.allow` list you derived in Step 2:

```yaml
  allow:
    - registry-1.docker.io
    - auth.docker.io
    - production.cloudflare.docker.com
    # ... only hosts with evidence
```

Add an inline comment for each host stating which Dockerfile line requires it.

### Enforcement scope for worktree sandboxes

**`egress.allow` is NOT enforced for worktree sandboxes.**

Every worktree sandbox the herdr plugin creates is launched with `--egress open`
(`internal/cli/cmd_herdr_plugin.go:3764`). At runtime that flag sets `OpenEgress: true`,
and `internal/core/service/service.go:959` responds by calling `AllowAllFor(72h)` —
bypassing the netfilter ACL entirely. The `AllowedHosts` list is stored in the envelope
but never consulted as a gate.

**What IS enforced for worktree sandboxes regardless of `OpenEgress`:**
- `egress.secrets` credential brokering — the guest holds a 64-hex placeholder, never the real token.
- `egress.policy.paths` for `SecretHosts` (e.g. GitHub REST path ACL, cross-repo 403, GraphQL 403) — enforced by the MITM proxy independently of the host ACL.

**Why still author the `egress.allow` list:**
- It IS enforced on sandboxes created via `nexus3 sandbox create` without `--egress open` (manually-created or CI sandboxes).
- It documents build-time network intent and becomes the active ACL if the open-egress posture changes.
- Do not present it as a security boundary for today's worktree sandboxes.

For the full enforcement model, verification probes, and live evidence, see `egress.md`.

### 3c. Complete file shape

The valid keys at the top level of `.nexus/config.yaml` are: `version`, `egress`, `sandbox`, `image`, `builder`. Any unknown key is a **hard parse error** (the parser enforces `KnownFields(true)`). The valid keys under `egress` are: `allow`, `policy`, `secrets`. A typo in a key name is therefore a hard failure, not a silently dropped entry.

---

## Step 4 — Author .nexus/Containerfile

Create `.nexus/Containerfile`. This is the guest image the sandbox boots from.

Minimal baseline for a repo that uses Docker Compose:

```dockerfile
FROM docker.io/library/ubuntu:24.04
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl git iproute2 iptables \
        docker.io docker-compose-v2 \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /workspace
ENV DOCKER_INSECURE_NO_IPTABLES_RAW=1
# Boot task: backgrounds dockerd and polls until ready.
RUN printf '%s\n' \
    '#!/bin/sh' \
    'set -e' \
    'mount --make-shared / 2>/dev/null || true' \
    'docker info >/dev/null 2>&1 && exit 0' \
    'dockerd --storage-driver=overlay2 >/tmp/nexus-dockerd.log 2>&1 &' \
    'for i in $(seq 1 30); do docker info >/dev/null 2>&1 && exit 0; sleep 0.5; done' \
    'echo "dockerd not ready; see /tmp/nexus-dockerd.log" >&2; exit 1' \
    > /usr/local/bin/nexus-dockerd-up \
 && chmod +x /usr/local/bin/nexus-dockerd-up
ENTRYPOINT ["/usr/local/bin/nexus-dockerd-up"]
```

For repos with a different stack (Go, Node, Python) but no Docker Compose, use a minimal base with just `ca-certificates curl git` and no ENTRYPOINT.

The working tree is **not** baked into the image — nexus3 mounts it live at runtime via virtiofs. Do not add `COPY . /workspace`.

---

## Step 5 — Validate the config before proposing it

nexus3 has no standalone `config show` command. Validate by triggering any nexus3 operation that reads the config from the repo directory. The simplest is `image build` with a nonexistent context, which exits early but parses the config first:

```sh
~/.local/bin/nexus3 image build -workspace <path-to-repo> 2>&1 | head -5
```

A parse error prints the offending key and exits nonzero. A missing `.nexus/config.yaml` is not an error (the binary proceeds without it). A present but malformed file is a hard error.

Alternatively, use Python with `pip install pyyaml` to confirm the YAML is structurally valid before the binary sees it:

```sh
python3 - << 'EOF'
import sys, yaml
with open(".nexus/config.yaml") as f:
    d = yaml.safe_load(f)
assert d.get("version") == 1, "missing version: 1"
top_known = {"version","egress","sandbox","image","builder"}
assert not set(d) - top_known, f"unknown keys: {set(d) - top_known}"
egress_known = {"allow","policy","secrets"}
e = d.get("egress", {}) or {}
assert not set(e) - egress_known, f"unknown egress keys: {set(e) - egress_known}"
print("OK")
EOF
```

Do not use the `./nexus3` binary in the nexus3 repo root — it is a leftover from an earlier explicit build and may be arbitrarily stale.

---

## Step 6 — Trust anchor: propose → merge → ratify

**The config is inert until it is on the default branch.**

The worktree sandbox launch reads `.nexus/config.yaml` from `refs/remotes/origin/HEAD` — the operator's default branch as seen from the local clone. It never reads the agent's checked-out feature branch.

Consequence:

1. Author `.nexus/config.yaml` on a feature branch and open a PR.
2. The PR branch config grants **nothing** — the sandbox boots with no egress rules from this file.
3. Operator reviews, confirms the path scoping is correct, and merges to the default branch.
4. From that point, every new worktree sandbox picks up the brokering and path-policy rules from `egress.secrets` and `egress.policy` (see the enforcement note in Step 3b for what `egress.allow` does and does not enforce).

Existing sandboxes are **not updated** automatically. They must be relaunched (`nexus3 rm` + `nexus3 create`) to pick up the merged config.

Tell the operator: "This config takes effect for new sandboxes only once merged to `origin/HEAD`. Existing sandboxes must be relaunched."

---

## Quick checklist

Before opening the PR:

- [ ] `version: 1` is the first key
- [ ] `api.github.com` paths are scoped to `/repos/OWNER/REPO/...` — no `/**` at root
- [ ] `/graphql` is absent from all `api.github.com` paths
- [ ] `egress.allow` entries have a Dockerfile/compose comment justifying each host
- [ ] Step 5 validation passes (no parse error from the binary; the YAML check prints `OK`)
- [ ] `.nexus/Containerfile` exists (or the operator has confirmed no custom image is needed)
- [ ] PR description includes: "This config takes effect once merged to `origin/HEAD`."
