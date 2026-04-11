#!/usr/bin/env bash
set -euo pipefail

repo=""
workspace_name=""
objective=""
context=""
backend=""
handoff_command=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo) repo="$2"; shift 2 ;;
    --workspace-name) workspace_name="$2"; shift 2 ;;
    --objective) objective="$2"; shift 2 ;;
    --context) context="$2"; shift 2 ;;
    --backend) backend="$2"; shift 2 ;;
    --handoff-command) handoff_command="$2"; shift 2 ;;
    *) echo "Unknown argument: $1" >&2; exit 2 ;;
  esac
done

if [[ -z "$repo" || -z "$workspace_name" || -z "$objective" ]]; then
  echo "Required: --repo --workspace-name --objective" >&2
  exit 2
fi

create_args=(workspace create --repo "$repo" --name "$workspace_name" --profile default)
if [[ -n "$backend" ]]; then
  create_args+=(--backend "$backend")
fi
create_output="$(nexus "${create_args[@]}" 2>&1)"
printf "%s\n" "$create_output"

workspace_id="$(printf "%s\n" "$create_output" | sed -nE 's/.*\(id: ([^)]+)\).*/\1/p' | head -n1)"
worktree_path="$(printf "%s\n" "$create_output" | sed -nE 's/^local worktree:[[:space:]]+(.+)$/\1/p' | head -n1)"

if [[ -z "$workspace_id" ]]; then
  echo "Failed to parse workspace id from create output" >&2
  exit 1
fi

nexus workspace start "$workspace_id"
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

echo "workspace_id=$workspace_id"
echo "worktree_path=$worktree_path"
echo "prompt_file=$prompt_file"

if [[ -n "$handoff_command" ]]; then
  resolved_command="${handoff_command//__WORKTREE__/$worktree_path}"
  resolved_command="${resolved_command//__PROMPT_FILE__/$prompt_file}"
  resolved_command="${resolved_command//__WORKSPACE_ID__/$workspace_id}"
  echo "executing_handoff_command=$resolved_command"
  eval "$resolved_command"
else
  echo "manual_handoff_command=cd \"$worktree_path\" && opencode \"\$(cat \"$prompt_file\")\""
fi
