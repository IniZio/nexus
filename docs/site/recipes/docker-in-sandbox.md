---
title: "Docker in a Sandbox"
description: "Run Docker and Compose inside a sandbox using declarative readiness-gated service startup"
---

# Docker in a Sandbox <Badge type="danger" text="not built" />

> Declare dockerd as a readiness-gated startup service so `nexus3 create` blocks until Docker is ready — no manual start, no poll loop.

<Badge type="warning" text="partial" /> — current implementation uses `nexus3 sandbox create`; see [CLI sandbox commands](/cli/sandbox-commands) for the mapping.

nexus3 sandboxes are workload-agnostic Linux VMs. Install Docker into the guest image, declare it
as a **startup service** <Badge type="danger" text="not built" />, and `nexus3 create` blocks
until the ready probe passes. The next command can use `docker` immediately.

**1. Add Docker to the guest image**

Create `.nexus/Containerfile` in your project root:

```dockerfile
# .nexus/Containerfile
FROM debian:bookworm-slim

RUN apt-get update -qq && \
    apt-get install -y --no-install-recommends \
        ca-certificates curl gnupg lsb-release && \
    install -m 0755 -d /etc/apt/keyrings && \
    curl -fsSL https://download.docker.com/linux/debian/gpg \
        -o /etc/apt/keyrings/docker.gpg && \
    chmod a+r /etc/apt/keyrings/docker.gpg && \
    echo \
      "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] \
       https://download.docker.com/linux/debian \
       $(lsb_release -cs) stable" \
      > /etc/apt/sources.list.d/docker.list && \
    apt-get update -qq && \
    apt-get install -y --no-install-recommends \
        docker-ce docker-ce-cli containerd.io && \
    rm -rf /var/lib/apt/lists/*

COPY .nexus/services.yaml /etc/nexus3/services.yaml
```

**2. Declare dockerd as a startup service** <Badge type="danger" text="not built" />

Create `.nexus/services.yaml` alongside the Containerfile:

```yaml
# .nexus/services.yaml
services:
  - name: dockerd
    command: [dockerd, --storage-driver=overlay2, --iptables=false]
    ready: [docker, info]
    restart: never
```

- `--storage-driver=overlay2` — the nexus3 guest kernel supports overlayfs.
- `--iptables=false` — Docker's iptables rules are redundant inside the nexus3 network namespace
  and can interfere with the host perimeter; disable them.

`ready: [docker, info]` is the readiness probe. `nexus3 create` polls it and returns only once
it exits zero, with a 30-second cap after which `create` fails. There is no poll loop to write.

The `.nexus/services.yaml` baked into the image is the canonical service declaration. The `--service 'name:cmd[:probe]'` flag on `nexus3 create` is a per-sandbox addition or override — it takes precedence over a same-named image entry without requiring an image rebuild. Image-level entries that are not overridden are unchanged. <Badge type="danger" text="not built" />

**3. Create the sandbox** <Badge type="danger" text="not built" />

```sh
nexus3 create myproject/my-sandbox --context .
# blocks ~2 s, prints "starting dockerd... ready"

nexus3 exec myproject/my-sandbox -- docker ps
# works immediately — no race, no sleep loop
```

<Badge type="warning" text="partial" /> — current implementation uses `--file`; see [CLI sandbox commands](/cli/sandbox-commands) for the mapping.

Use `--dockerfile` if the Containerfile is at a non-default path:

```sh
nexus3 create myproject/my-sandbox \
  --context . \
  --dockerfile .nexus/Containerfile.docker
```

**4. Run containers or Compose**

```sh
# Single container
nexus3 exec myproject/my-sandbox -- docker run --rm hello-world

# docker compose (compose.yml must be present in the guest workspace)
nexus3 exec myproject/my-sandbox -- docker compose up -d
```

---

## The `--nested` flag

`nexus3 create --nested` enables `/dev/kvm` passthrough so the guest can run nested VMs.
Docker and `docker compose` do **not** need `--nested`; the flag is only relevant if containers
or scripts themselves require hardware virtualisation (rare). Leave it out unless you have a
specific KVM requirement.

---

## Boot-time cost <Badge type="danger" text="not built" />

- `nexus3 create` blocks until the `docker info` probe passes. Socket readiness is observed at
  roughly **2 s** after the sandbox boots (depends on host load and image size).
- The 30-second cap is absolute: if `docker info` has not passed by then, `create` fails with a
  service-ready-timeout error. Check `/tmp/dockerd.log` inside the sandbox for diagnostics.

---

## Security posture

- Default-deny egress is enforced by the nexus3 host perimeter. Running dockerd inside the
  sandbox does not change the sandbox's outbound network policy — all traffic still flows through
  the nexus3 MITM perimeter.
- Do **not** bake credentials (registry auth, secret env vars) into the guest image via the
  Containerfile. Inject secrets at runtime via `nexus3 exec` environment variables or the
  credential broker.
- `--iptables=false` is recommended (see above). Docker's iptables rules inside the guest can
  create unexpected interactions with the nexus3 perimeter netstack.

---

## Design rationale <Badge type="danger" text="not built" />

`nexus3 create` is readiness-gated: it returns only once every declared startup service has
passed its ready probe. This eliminates the race between sandbox creation and workload
availability. The guest agent (already PID 1) drives the service table; no init system is added.

The one exception is `nexus3 create --context`, which starts an in-VM **buildkitd** to build the
custom guest image. That is a nexus3 primitive, not user-workload Docker.
