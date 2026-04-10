#!/usr/bin/env bash
set -euo pipefail

path="."
objective=""
context=""
backend=""
nexus_bin=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --path) path="$2"; shift 2 ;;
    --objective) objective="$2"; shift 2 ;;
    --context) context="$2"; shift 2 ;;
    --backend) backend="$2"; shift 2 ;;
    --nexus-bin) nexus_bin="$2"; shift 2 ;;
    *) echo "Unknown argument: $1" >&2; exit 2 ;;
  esac
done

if [[ -z "$objective" ]]; then
  echo "Required: --objective" >&2
  exit 2
fi

resolve_nexus_bin() {
  if [[ -n "$nexus_bin" && -x "$nexus_bin" ]]; then
    printf "%s" "$nexus_bin"
    return 0
  fi
  if [[ -n "${NEXUS_BIN:-}" && -x "${NEXUS_BIN}" ]]; then
    printf "%s" "${NEXUS_BIN}"
    return 0
  fi
  if command -v nexus >/dev/null 2>&1; then
    command -v nexus
    return 0
  fi
  if [[ -x "packages/nexus/nexus" ]]; then
    printf "%s" "packages/nexus/nexus"
    return 0
  fi
  if [[ -x "./nexus" ]]; then
    printf "%s" "./nexus"
    return 0
  fi
  echo "Unable to locate nexus binary. Set NEXUS_BIN or pass --nexus-bin." >&2
  return 1
}

nexus_cmd="$(resolve_nexus_bin)"
if [[ "$nexus_cmd" != /* ]]; then
  nexus_cmd="$(pwd)/$nexus_cmd"
fi
repo_root="$(cd "$path" && pwd)"
create_args=(workspace create)
if [[ -n "$backend" ]]; then
  create_args+=(--backend "$backend")
fi
set +e
create_output="$(cd "$repo_root" && "$nexus_cmd" "${create_args[@]}" 2>&1)"
create_status=$?
set -e
printf "%s\n" "$create_output"
if [[ $create_status -ne 0 ]]; then
  exit $create_status
fi

workspace_id="$(printf "%s\n" "$create_output" | sed -nE 's/.*\(id: ([^)]+)\).*/\1/p' | head -n1)"
worktree_path="$(printf "%s\n" "$create_output" | sed -nE 's/^local worktree:[[:space:]]+(.+)$/\1/p' | head -n1)"

if [[ -z "$workspace_id" ]]; then
  echo "Failed to parse workspace id from create output" >&2
  exit 1
fi

set +e
start_output="$("$nexus_cmd" workspace start "$workspace_id" 2>&1)"
start_status=$?
set -e
printf "%s\n" "$start_output"
if [[ $start_status -ne 0 ]]; then
  exit $start_status
fi
tmp_prompt_base="$(mktemp -t "nexus-handoff-${workspace_id}")"
prompt_file="${tmp_prompt_base}.md"
mv "$tmp_prompt_base" "$prompt_file"

cat > "$prompt_file" <<EOF
Continue implementation in workspace \`$workspace_id\` at \`$worktree_path\`.

## Objective

$objective

## Context

$context
EOF

workspace_handoff_dir=""
workspace_prompt_rel=""
workspace_suggested_rel=""
if [[ -n "$worktree_path" ]]; then
  workspace_handoff_dir="$worktree_path/.nexus/handoff"
  mkdir -p "$workspace_handoff_dir"
  workspace_prompt_name="handoff-${workspace_id}.md"
  workspace_prompt_rel=".nexus/handoff/${workspace_prompt_name}"
  cp "$prompt_file" "$workspace_handoff_dir/$workspace_prompt_name"
fi

collect_plan_files() {
  local source_root="$1"
  if ! git -C "$source_root" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    return 0
  fi
  {
    git -C "$source_root" diff --name-only
    git -C "$source_root" diff --cached --name-only
    git -C "$source_root" ls-files --others --exclude-standard
  } | awk 'NF > 0' | sort -u | while IFS= read -r rel; do
    lower="$(printf "%s" "$rel" | tr '[:upper:]' '[:lower:]')"
    case "$lower" in
      *prd*|*plan*|docs/superpowers/specs/*|docs/*/plans/*)
        if [[ -f "$source_root/$rel" ]]; then
          printf "%s\n" "$rel"
        fi
        ;;
    esac
  done
}

copied_file_list="$(mktemp -t "nexus-handoff-copied-${workspace_id}")"
collect_plan_files "$repo_root" >"$copied_file_list"
if [[ -s "$copied_file_list" && -n "$worktree_path" ]]; then
  while IFS= read -r rel; do
    mkdir -p "$(dirname "$worktree_path/$rel")"
    cp "$repo_root/$rel" "$worktree_path/$rel"
  done <"$copied_file_list"
fi

echo "workspace_id=$workspace_id"
echo "worktree_path=$worktree_path"
echo "workspace_path=/workspace"
echo "prompt_file=$prompt_file"
if [[ -n "$worktree_path" ]]; then
  encoded_path="$(python3 - <<'PY' "$worktree_path"
import sys
from urllib.parse import quote
print(quote(sys.argv[1], safe="/"))
PY
)"
  echo "cursor_deeplink=cursor://file${encoded_path}"
  echo "vscode_deeplink=vscode://file${encoded_path}"
  echo "cursor_open_command=cursor \"$worktree_path\""
  echo "vscode_open_command=code \"$worktree_path\""
fi
if [[ -s "$copied_file_list" ]]; then
  echo "copied_plan_files=1"
  while IFS= read -r rel; do
    echo "copied_file=$rel"
  done <"$copied_file_list"
else
  echo "copied_plan_files=0"
fi
suggested_prompt_file="$(mktemp -t "nexus-handoff-suggested-${workspace_id}").md"
{
  echo "You are working inside /workspace for workspace $workspace_id."
  echo ""
  echo "Objective:"
  echo "$objective"
  echo ""
  if [[ -n "$context" ]]; then
    echo "Context:"
    echo "$context"
    echo ""
  fi
  if [[ -s "$copied_file_list" ]]; then
    echo "Read these copied PRD/plan files first:"
    while IFS= read -r rel; do
      echo "- $rel"
    done <"$copied_file_list"
    echo ""
  fi
  echo "Then propose a short implementation plan and begin execution."
} >"$suggested_prompt_file"
if [[ -n "$workspace_handoff_dir" ]]; then
  workspace_suggested_name="suggested-${workspace_id}.md"
  workspace_suggested_rel=".nexus/handoff/${workspace_suggested_name}"
  cp "$suggested_prompt_file" "$workspace_handoff_dir/$workspace_suggested_name"
fi
echo "suggested_prompt_file=$suggested_prompt_file"
if [[ -n "$workspace_prompt_rel" && -n "$workspace_suggested_rel" ]]; then
  echo "workspace_prompt_path=/workspace/$workspace_prompt_rel"
  echo "workspace_suggested_prompt_path=/workspace/$workspace_suggested_rel"
  echo "start_session_command=$nexus_cmd workspace ssh \"$workspace_id\" --command \"cd /workspace && opencode \\\"\\\$(cat /workspace/$workspace_suggested_rel)\\\"\""
  echo "continue_last_session_command=$nexus_cmd workspace ssh \"$workspace_id\" --command \"cd /workspace && opencode --continue\""
  echo "continue_with_session_id_command=$nexus_cmd workspace ssh \"$workspace_id\" --command \"cd /workspace && opencode --session \\\"<session-id>\\\"\""
  echo "manual_handoff_command=$nexus_cmd workspace ssh \"$workspace_id\" --command \"cd /workspace && opencode \\\"\\\$(cat /workspace/$workspace_prompt_rel)\\\"\""
else
  echo "start_session_command=cd \"$worktree_path\" && opencode \"\$(cat \"$suggested_prompt_file\")\""
  echo "continue_last_session_command=cd \"$worktree_path\" && opencode --continue"
  echo "continue_with_session_id_command=cd \"$worktree_path\" && opencode --session \"<session-id>\""
  echo "manual_handoff_command=cd \"$worktree_path\" && opencode \"\$(cat \"$prompt_file\")\""
fi
