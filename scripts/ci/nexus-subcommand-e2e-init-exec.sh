#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/../../packages/nexus"

if [[ "$(uname -s)" == "Darwin" ]]; then
  go test ./cmd/nexus -count=1 -run "TestRunInit|TestDarwinBootstrap|TestEnsurePersistentLimaInstance"
else
  go test ./cmd/nexus -count=1 -run "TestRunInit|TestRunExec"
fi
