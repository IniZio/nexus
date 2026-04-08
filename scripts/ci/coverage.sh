#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/../../packages/nexus"
go test ./... -covermode=atomic -coverprofile=coverage.out
go tool cover -func=coverage.out | tail -n 1
