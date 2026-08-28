# PROTOTYPE — experimental, do not promote. See README.md.
#
# Multi-stage build that packages the nexus3 HOST daemon so it can boot
# microVMs from inside `docker run` (same shape as microsandbox's msbserver).
#
# Build context: nexus3 repo root  (use .dockerignore to keep it trim).
#   docker build -f examples/nexus3-in-docker/Dockerfile -t nexus3-host:latest .
#
# Runtime deps summary:
#   nexus3          — glibc-dynamic Go binary (built from source here)
#   cloud-hypervisor — static-pie, downloaded from GitHub releases (pinned)
#   virtiofsd       — static-pie, see TODO below
#   gvproxy         — IN-PROCESS (vendored into nexus3), no separate binary
#   mke2fs          — from e2fsprogs (apt)
#   iproute2/iptables — for egress netns perimeter (apt)

# ─────────────────────────────────────────────────────────────────────────────
# Stage 1 — build the nexus3 host binary from source
# ─────────────────────────────────────────────────────────────────────────────
FROM golang:1.25.5-bookworm AS go-builder

WORKDIR /src
# go.mod has a local replace directive for third_party/gvisor-tap-vsock,
# so that directory must be present before `go mod download` can resolve it.
COPY go.mod go.sum ./
COPY third_party/ third_party/
RUN go mod download

COPY cmd/      cmd/
COPY internal/ internal/
COPY proto/    proto/
COPY plugins/  plugins/

RUN CGO_ENABLED=1 go build -trimpath -o /out/nexus3 ./cmd/nexus3

# ─────────────────────────────────────────────────────────────────────────────
# Stage 2 — download pinned cloud-hypervisor static binary
#
# cloud-hypervisor v52.0 — static-pie, no runtime deps.
# SHA-256 verified to catch corruption or tampering.
# ─────────────────────────────────────────────────────────────────────────────
FROM debian:bookworm-slim AS downloader

RUN apt-get update && apt-get install -y --no-install-recommends \
        curl \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/*

ARG CH_VERSION=v52.0
ARG CH_SHA256=829af01ff075bb96c4f183905134c453a88d68cbabdc6b87df21098842581ee9

RUN curl -fsSL \
        "https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/${CH_VERSION}/cloud-hypervisor-static" \
        -o /usr/local/bin/cloud-hypervisor \
    && echo "${CH_SHA256}  /usr/local/bin/cloud-hypervisor" | sha256sum -c - \
    && chmod +x /usr/local/bin/cloud-hypervisor

# ─────────────────────────────────────────────────────────────────────────────
# Stage 3 — virtiofsd
#
# virtiofsd v1.13.3 is a Rust static-pie binary.  The official GitLab project
# (virtio-fs/virtiofsd) ships only source archives in its releases — no
# pre-built binary assets.  Building from source requires a full Rust toolchain
# (~1 GB layer); we defer that to a follow-up and copy the host binary for now.
#
# TODO: add a `FROM rust:bookworm AS rust-builder` stage that runs
#   `cargo install virtiofsd --version 1.13.3 --root /out`
# once build-time is acceptable, or wait for GitLab to publish binary assets.
# ─────────────────────────────────────────────────────────────────────────────
FROM debian:bookworm-slim AS virtiofsd-stage

# virtiofsd binary staged by build.sh from the host (static-pie, runs on any
# glibc base).  SHA-256 of the host binary (virtiofsd 1.13.3):
#   7ef50584b2dce226f994ac99aa86fbf603d4d61cd6e8caaffd0b005ce4796024
COPY examples/nexus3-in-docker/stage/virtiofsd /usr/local/bin/virtiofsd
RUN chmod +x /usr/local/bin/virtiofsd

# ─────────────────────────────────────────────────────────────────────────────
# Stage 4 — runtime image (glibc required by nexus3)
# ─────────────────────────────────────────────────────────────────────────────
FROM debian:bookworm-slim AS runtime

RUN apt-get update && apt-get install -y --no-install-recommends \
        e2fsprogs \
        iproute2 \
        iptables \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Binaries
COPY --from=go-builder   /out/nexus3                   /usr/local/bin/nexus3
COPY --from=downloader   /usr/local/bin/cloud-hypervisor /usr/local/bin/cloud-hypervisor
COPY --from=virtiofsd-stage /usr/local/bin/virtiofsd   /usr/local/bin/virtiofsd

# Guest kernel + agent baked in from the repo (images/ included in build context)
COPY images/kernel/vmlinux-x86_64 /opt/nexus3/images/kernel/vmlinux-x86_64
COPY images/kernel/nexus3-agent   /opt/nexus3/images/kernel/nexus3-agent

# nexus3 resolves the kernel via this env var (explicit override always wins).
ENV NEXUS3_KERNEL_PATH=/opt/nexus3/images/kernel/vmlinux-x86_64
ENV PATH=/usr/local/bin:/usr/local/sbin:/usr/sbin:/usr/bin:/sbin:/bin

ENTRYPOINT ["nexus3"]
CMD ["doctor"]
