#!/usr/bin/env bash
# PROTOTYPE — run the nexus3 host daemon inside a container.
#
# MINIMAL capability set (not --privileged):
#   --device /dev/kvm       hypervisor accelerator — hard requirement
#   --device /dev/net/tun   TAP devices for the in-process gvproxy/netns perimeter
#   --cap-add NET_ADMIN     create netns + TAP + program netfilter rules
#   --cap-add SYS_ADMIN     unshare(CLONE_NEWNET) inside the egress supervisor
#
# Omit --privileged intentionally; the microVM is the isolation boundary.
#
# GOTCHA 1 — STATE OWNERSHIP:
#   Mount a fresh container-owned Docker volume at /root/.local/state/nexus3.
#   If you bind-mount the HOST's ~/.local/state/nexus3 the disk images are owned
#   by the host uid; cloud-hypervisor opens them O_RDWR and gets
#   "Permission denied (os error 13) — The VM could not boot".
#
# GOTCHA 2 — WORKTREE TMPDIR:
#   `create --file` refuses when TMPDIR is on a different device than the source
#   tree, and loops infinitely if TMPDIR is inside the source tree.  Put source
#   and TMPDIR as SIBLINGS on the SAME volume (e.g. /work/src + /work/tmp).
#
# GOTCHA 3 — SOCKET PATH CONSISTENCY (XDG_RUNTIME_DIR):
#   nexus3 has two socket-dir formulas: the CLI uses $TMPDIR, the CHDriver
#   fallback hardcodes /tmp.  Set XDG_RUNTIME_DIR (takes precedence in BOTH
#   codepaths) to a writable path on the work volume so all components agree.
#   Without this, `create` puts sockets in /work/rt/nexus3/ but `exec` looks
#   in /tmp/nexus3-0/ and every exec fails with "no such file or directory".
set -euo pipefail

IMAGE="${NEXUS3_IMAGE:-nexus3-host:latest}"

exec docker run --rm -it \
    --device /dev/kvm \
    --device /dev/net/tun \
    --cap-add NET_ADMIN \
    --cap-add SYS_ADMIN \
    "$IMAGE" "${@:-doctor}"
