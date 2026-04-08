#!/usr/bin/env bash
set -euo pipefail

go_files="$(git ls-files '*.go')"
if [ -z "${go_files}" ]; then
  echo "No Go files found; skipping go fix check"
  exit 0
fi

tmp_diff="$(mktemp)"
# shellcheck disable=SC2086
go tool fix -go=go1.24 -diff ${go_files} >"${tmp_diff}"

if [ -s "${tmp_diff}" ]; then
  echo "go tool fix reported changes. Please run: go tool fix -go=go1.24 <files>"
  cat "${tmp_diff}"
  exit 1
fi
