#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/../.."
pnpm install
task build
task lint
task test
