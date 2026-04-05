#!/usr/bin/env bash
set -euo pipefail

go test ./packages/nexus/cmd/nexus -count=1
go test ./packages/nexus/pkg/runtime/firecracker -count=1
