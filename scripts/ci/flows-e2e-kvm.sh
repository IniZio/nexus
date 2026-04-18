#!/usr/bin/env bash
set -euo pipefail
if [[ -e /dev/kvm ]]; then
  sudo chmod 666 /dev/kvm || true
fi
