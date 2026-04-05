#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

bash "${ROOT}/.nexus/check/20-cli-subcommands.sh"
bash "${ROOT}/.nexus/check/30-package-tests.sh"
