#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/../../packages/nexus"
# cmd/nexus and cmd/nexus-firecracker-agent import pkg/* removed in rewrite; exclude until CLI rewired
go test ./internal/... ./cmd/nexusd/ \
  -covermode=atomic -coverprofile=coverage.out
go tool cover -func=coverage.out | tail -n 1
