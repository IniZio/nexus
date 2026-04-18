#!/usr/bin/env bash
set -euo pipefail

DEFAULT_ALLOW=(
  "packages/nexus/pkg/server/server.go"
  "packages/nexus/pkg/handlers/workspace_manager.go"
)

extra_allow=()
files=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    --allow | --allow-list)
      flag=$1
      shift
      if [[ $# -eq 0 ]]; then
        echo "check-file-sizes: missing path after $flag" >&2
        exit 2
      fi
      extra_allow+=("$1")
      shift
      ;;
    *)
      files+=("$1")
      shift
      ;;
  esac
done

repo_root=$(git rev-parse --show-toplevel 2>/dev/null) || {
  echo "check-file-sizes: not inside a git repository" >&2
  exit 2
}
cd "$repo_root"

if [[ ${#files[@]} -eq 0 ]]; then
  while IFS= read -r line || [[ -n "${line:-}" ]]; do
    [[ -n "${line:-}" ]] && files+=("$line")
  done < <(git diff --name-only HEAD)
fi

to_rel() {
  local f="$1"
  f="${f#./}"
  case "$f" in
    "$repo_root"/*) f="${f#"$repo_root"/}" ;;
  esac
  printf '%s\n' "$f"
}

is_allowed() {
  local p="$1"
  local a
  for a in "${DEFAULT_ALLOW[@]}"; do
    [[ "$p" == "$a" ]] && return 0
  done
  if ((${#extra_allow[@]} > 0)); then
    for a in "${extra_allow[@]}"; do
      [[ "$p" == "$a" ]] && return 0
    done
  fi
  return 1
}

should_check() {
  local path="$1"
  case "$path" in
  *"/vendor/"* | *"/node_modules/"*) return 1 ;;
  esac
  case "$path" in
  *.pb.go) return 1 ;;
  esac
  local bn
  bn=$(basename "$path")
  case "$bn" in
  *_test.go | *.test.ts | *.spec.ts) return 1 ;;
  esac
  case "$path" in
  *.go | *.ts) return 0 ;;
  esac
  return 1
}

threshold_for() {
  local path="$1"
  local base
  base=$(basename "$path")
  case "$base" in
  types.go | types.ts)
    echo 500
    return
    ;;
  esac
  case "$path" in
  */transport/* | */storage/* | */adapters/*)
    echo 500
    return
    ;;
  esac
  case "$path" in
  */domain/* | */entities/* | */models/*)
    echo 300
    return
    ;;
  esac
  echo 400
}

fail=0

for raw in "${files[@]}"; do
  [[ -z "$raw" ]] && continue
  rel=$(to_rel "$raw")
  [[ -f "$rel" ]] || continue
  if ! should_check "$rel"; then
    continue
  fi
  lines=$(wc -l <"$rel" | tr -d ' ')
  if is_allowed "$rel"; then
    echo "$rel: skipped (known debt)"
    continue
  fi
  limit=$(threshold_for "$rel")
  if ((lines > limit)); then
    over=$((lines - limit))
    echo "$rel: $lines lines exceeds limit $limit by $over" >&2
    fail=1
  fi
done

exit "$fail"
