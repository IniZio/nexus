# Docker in a Sandbox

nexus3 sandboxes are workload-agnostic. The sandbox provides a Linux VM with
a kernel, a root filesystem, and a network namespace — nothing more. If your
workload needs Docker, you install it into the guest image and start the
daemon yourself. This guide shows you how.

---

## 1. Add Docker to the guest image

Create `.nexus/Containerfile` in your project root. This file uses OCI
Containerfile syntax and is automatically picked up when you pass `--file .`
to `sandbox create`. The builder VM's buildkitd executes it to produce the
sandbox rootfs.

```dockerfile
# .nexus/Containerfile
#
# Substitute the FROM image for your actual base. This example starts
# from debian:bookworm-slim and adds the upstream Docker Engine.

FROM debian:bookworm-slim

# Install Docker Engine from the official apt repository.
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
```

Build and boot the sandbox. The positional argument is `<project>/<name>` and
`--file <dir>` points to the directory containing `.nexus/Containerfile`:

```sh
nexus3 sandbox create myproject/my-sandbox --file .
```

If your Containerfile is at a non-default path, use `--dockerfile` to
override it (always combined with `--file`):

```sh
nexus3 sandbox create myproject/my-sandbox --file . --dockerfile .nexus/Containerfile.docker
```

---

## 2. Start dockerd inside the sandbox

nexus3 does not auto-start any workload daemon — that is the workload's job.
After the sandbox is running, start dockerd yourself:

```sh
# Option A: exec a one-liner from the host
nexus3 exec myproject/my-sandbox -- sh -c \
  'mount --make-shared / 2>/dev/null || true
   docker info >/dev/null 2>&1 && exit 0
   dockerd --storage-driver=overlay2 --iptables=false \
       >/tmp/dockerd.log 2>&1 &'

# Option B: open an interactive shell and run commands manually
nexus3 shell myproject/my-sandbox
# inside the sandbox shell:
mount --make-shared / 2>/dev/null || true
dockerd --storage-driver=overlay2 --iptables=false >/tmp/dockerd.log 2>&1 &
```

Flags used:
- `--storage-driver=overlay2` — the nexus3 guest kernel supports overlayfs.
- `--iptables=false` — the guest runs inside a nexus3 network namespace with
  default-deny egress enforced by the host perimeter. Docker's iptables rules
  are redundant and can interfere with the perimeter; disable them.

---

## 3. Verify dockerd is ready

Poll until `docker info` succeeds (dockerd is typically ready within 5 s of
the `dockerd` command):

```sh
nexus3 exec myproject/my-sandbox -- sh -c \
  'i=0; until docker info >/dev/null 2>&1; do
     i=$((i+1)); [ $i -ge 30 ] && { echo "dockerd not ready after 30s"; exit 1; }
     sleep 1
   done; echo "dockerd ready"'
```

Or, from inside an attached shell:

```sh
docker info
```

If dockerd has not started yet, check the daemon log:

```sh
nexus3 exec myproject/my-sandbox -- cat /tmp/dockerd.log
```

---

## 4. Run containers or Compose

Once `docker info` succeeds:

```sh
# Single container
nexus3 exec myproject/my-sandbox -- docker run --rm hello-world

# docker compose (compose.yml must be in the guest workspace)
nexus3 exec myproject/my-sandbox -- docker compose up -d
```

---

## 5. The `--nested` flag

`nexus3 sandbox create --nested` enables `/dev/kvm` passthrough so the guest
can run nested VMs. Docker and `docker compose` do **not** need `--nested`;
the flag is only relevant if your containers or scripts themselves require
hardware virtualisation (rare). Leave it out unless you have a specific KVM
requirement.

---

## Boot-time cost (measured data)

- The sandbox boot sequence has no docker-related overhead — dockerd is not
  started by nexus3.
- When you start dockerd manually, socket readiness is observed at roughly
  **5 s** after the `dockerd` command returns (compose pilot measurements,
  2026-08-14).
- If you need dockerd to be ready immediately after sandbox creation, exec the
  start-and-wait script above as the first step of your workflow.

---

## Security posture

- Default-deny egress is enforced by the nexus3 host perimeter. Running
  dockerd inside the sandbox does not change the sandbox's outbound network
  policy — all traffic still flows through the nexus3 MITM perimeter.
- Do **not** bake credentials (registry auth, secret env vars) into the guest
  image via the Containerfile. Inject secrets at runtime via `nexus3 exec`
  environment variables or the credential broker.
- `--iptables=false` is recommended (see above). Docker's iptables rules
  inside the guest can create unexpected interactions with the nexus3
  perimeter netstack.

---

## Design rationale

nexus3 follows the same principle as microsandbox and OpenShell: the sandbox
is a primitive — it provides a VM, not an opinionated runtime. Docker, Compose,
or any other daemon is the workload's responsibility to install and start. This
is decision D-PD-21: nexus3 ships generic primitives, never workflow opinion.

The one exception is `sandbox create --file`, which starts an in-VM
**buildkitd** to build a custom guest image. That is a nexus3 primitive (the
`--file` flag), not user-workload docker.
