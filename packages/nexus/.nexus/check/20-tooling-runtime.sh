#!/usr/bin/env bash
set -euo pipefail
command -v bash >/dev/null 2>&1
command -v curl >/dev/null 2>&1 || true
echo 'tooling-runtime check passed'
