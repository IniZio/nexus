#!/usr/bin/env bash
set -euo pipefail

backend="${1:-}"

if [ "$backend" = "firecracker" ]; then
  echo "cleanup backend: native firecracker resources are managed by Nexus daemon; action cleanup is a no-op"
  exit 0
fi

if [ "$backend" != "lxc" ]; then
  exit 0
fi

instance_name="${NEXUS_DOCTOR_LXC_INSTANCE:-}"
if [ -z "$instance_name" ]; then
  echo "cleanup backend: lxc instance not configured; skipping"
  exit 0
fi

if ! command -v lxc >/dev/null 2>&1; then
  echo "cleanup backend: lxc command not available; skipping"
  exit 0
fi

echo "+ sudo lxc delete --force $instance_name"
if ! sudo lxc delete --force "$instance_name"; then
  echo "cleanup backend: failed to remove lxc instance $instance_name"
fi
