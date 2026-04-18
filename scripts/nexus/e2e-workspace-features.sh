#!/usr/bin/env bash
set -euo pipefail

daemon_port="${NEXUS_DAEMON_PORT:-8899}"
daemon_token="${NEXUS_DAEMON_TOKEN:-test-token}"
repo_path="${1:-/Users/newman/magic/nexus/.case-studies/spotlight-compose-e2e-v2}"
base_name="e2e-features-$(date +%s)"
workspace_name="${base_name}"
child_name="${base_name}-child"
handoff_name="${base_name}-handoff"

pick_free_port() {
  local start="$1"
  local end="$2"
  local p
  for ((p=start; p<=end; p++)); do
    if ! lsof -nP -iTCP:"$p" -sTCP:LISTEN >/dev/null 2>&1; then
      echo "$p"
      return 0
    fi
  done
  return 1
}

spotlight_remote_port="$(pick_free_port 18080 18999)"
spotlight_local_port="$(pick_free_port 19080 19999)"

if [[ ! -d "$repo_path" ]]; then
  echo "repo path not found: $repo_path" >&2
  exit 2
fi

nexus_bin="${NEXUS_BIN:-/tmp/nexus-e2e/nexus}"
if [[ ! -x "$nexus_bin" ]]; then
  nexus_bin="$(command -v nexus)"
fi

workspace_ids=()
cleanup() {
  for ws in "${workspace_ids[@]}"; do
    NEXUS_DAEMON_PORT="$daemon_port" NEXUS_DAEMON_TOKEN="$daemon_token" "$nexus_bin" workspace remove "$ws" >/dev/null 2>&1 || true
  done
}
trap cleanup EXIT

echo "== create workspace =="
create_output="$(NEXUS_DAEMON_PORT="$daemon_port" NEXUS_DAEMON_TOKEN="$daemon_token" "$nexus_bin" workspace create --repo "$repo_path" --name "$workspace_name" --backend firecracker)"
echo "$create_output"
workspace_id="$(printf "%s\n" "$create_output" | sed -nE 's/.*\(id: ([^)]+)\).*/\1/p' | head -n1)"
worktree_path="$(printf "%s\n" "$create_output" | sed -nE 's/^local worktree:[[:space:]]+(.+)$/\1/p' | head -n1)"
workspace_ids+=("$workspace_id")
NEXUS_DAEMON_PORT="$daemon_port" NEXUS_DAEMON_TOKEN="$daemon_token" "$nexus_bin" workspace start "$workspace_id"

echo "== tools in workspace =="
tool_output="$(NEXUS_DAEMON_PORT="$daemon_port" NEXUS_DAEMON_TOKEN="$daemon_token" "$nexus_bin" workspace ssh "$workspace_id" --command 'for t in opencode codex claude; do if command -v "$t" >/dev/null 2>&1; then echo "$t=present"; else echo "$t=missing"; fi; done')"
echo "$tool_output"
printf "%s\n" "$tool_output" | rg "opencode=present|codex=present|claude=present" >/dev/null

echo "== sync host -> workspace and workspace -> host =="
host_marker="$worktree_path/.nexus_host_sync.txt"
remote_marker="$worktree_path/.nexus_remote_sync.txt"
printf "host-sync-%s\n" "$(date +%s)" > "$host_marker"
NEXUS_DAEMON_PORT="$daemon_port" NEXUS_DAEMON_TOKEN="$daemon_token" "$nexus_bin" workspace ssh "$workspace_id" --command "cat \"$host_marker\" && echo remote-sync-$(date +%s) > \"$remote_marker\" && cat \"$remote_marker\""
[[ -f "$remote_marker" ]]

echo "== run docker compose service =="
printf "HOST_HTTP_PORT=%s\n" "$spotlight_remote_port" > "$worktree_path/.env"
NEXUS_DAEMON_PORT="$daemon_port" NEXUS_DAEMON_TOKEN="$daemon_token" "$nexus_bin" workspace ssh "$workspace_id" --command "cd \"$worktree_path\" && printf \"HOST_HTTP_PORT=%s\\n\" \"$spotlight_remote_port\" > .env && docker compose up -d && docker compose ps && curl -sS http://127.0.0.1:$spotlight_remote_port"

echo "== spotlight expose/close with host reachability proof =="
curl -sS --max-time 5 "http://127.0.0.1:$spotlight_local_port" >/dev/null 2>&1 && {
  echo "expected spotlight local port $spotlight_local_port to be free" >&2
  exit 1
} || true
forward_json="$(node -e 'const WebSocket=require("ws");const ws=new WebSocket(`ws://127.0.0.1:'"$daemon_port"'/?token='"$daemon_token"'`);ws.on("open",()=>ws.send(JSON.stringify({jsonrpc:"2.0",id:"1",method:"spotlight.start",params:{spec:{workspaceId:"'"$workspace_id"'",service:"compose-web",remotePort:'"$spotlight_remote_port"',localPort:'"$spotlight_local_port"',host:"127.0.0.1"}}})));ws.on("message",m=>{console.log(String(m));process.exit(0);});')"
echo "$forward_json"
forward_id="$(printf "%s\n" "$forward_json" | node -e 'let s="";process.stdin.on("data",d=>s+=d);process.stdin.on("end",()=>{const j=JSON.parse(s);console.log(j.result.forward.id);});')"
curl -sS --max-time 10 "http://127.0.0.1:$spotlight_local_port"
node -e 'const WebSocket=require("ws");const ws=new WebSocket(`ws://127.0.0.1:'"$daemon_port"'/?token='"$daemon_token"'`);ws.on("open",()=>ws.send(JSON.stringify({jsonrpc:"2.0",id:"1",method:"spotlight.close",params:{id:"'"$forward_id"'"}})));ws.on("message",m=>{console.log(String(m));process.exit(0);});'
curl -sS --max-time 5 "http://127.0.0.1:$spotlight_local_port" >/dev/null 2>&1 && {
  echo "expected spotlight local port $spotlight_local_port to close after spotlight.close" >&2
  exit 1
} || true

echo "== fork workspace =="
fork_output="$(NEXUS_DAEMON_PORT="$daemon_port" NEXUS_DAEMON_TOKEN="$daemon_token" "$nexus_bin" workspace fork --id "$workspace_id" --name "$child_name")"
echo "$fork_output"
child_id="$(printf "%s\n" "$fork_output" | sed -nE 's/.*\(id: ([^)]+)\).*/\1/p' | head -n1)"
workspace_ids+=("$child_id")
NEXUS_DAEMON_PORT="$daemon_port" NEXUS_DAEMON_TOKEN="$daemon_token" "$nexus_bin" workspace start "$child_id"

echo "== handoff skill script =="
handoff_output="$(PATH="/tmp/nexus-e2e:$PATH" NEXUS_DAEMON_PORT="$daemon_port" NEXUS_DAEMON_TOKEN="$daemon_token" bash skills/nexus/handoff/scripts/create_workspace_handoff.sh --repo "$repo_path" --workspace-name "$handoff_name" --objective "e2e handoff check" --backend firecracker --context "e2e-workspace-features.sh verification")"
echo "$handoff_output"
handoff_id="$(printf "%s\n" "$handoff_output" | sed -nE 's/^workspace_id=(.+)$/\1/p' | head -n1)"
workspace_ids+=("$handoff_id")

echo "== done =="
echo "workspace_id=$workspace_id"
echo "child_id=$child_id"
echo "handoff_id=$handoff_id"
