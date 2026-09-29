#!/usr/bin/env bash
# S9b-8: end-to-end check on a host with kernel.apparmor_restrict_unprivileged_userns=1.
# Usage: scripts/s9b-restricted.sh [--state DIR] [--image REF]
# S9B8_ALLOW_UNRESTRICTED=1 bypasses the sysctl assertion (dry run only).
set -u

STATE=/var/tmp/s9b8
IMAGE=alpine:3.21
GIT_IMAGE=ghcr.io/inizio/nexus-base:latest
ROOT=$(cd "$(dirname "$0")/.." && pwd)
SYSCTL=/proc/sys/kernel/apparmor_restrict_unprivileged_userns

while [ $# -gt 0 ]; do
  case "$1" in
    --state) STATE=$2; shift 2 ;;
    --image) IMAGE=$2; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

val=$(cat "$SYSCTL" 2>/dev/null || echo missing)
if [ "$val" != 1 ] && [ "${S9B8_ALLOW_UNRESTRICTED:-0}" != 1 ]; then
  echo "$SYSCTL is '$val', need 1; refusing (set S9B8_ALLOW_UNRESTRICTED=1 for a dry run)" >&2
  exit 2
fi
command -v jq >/dev/null || { echo "jq required" >&2; exit 2; }

mkdir -p "$STATE/home" "$STATE/run" "$STATE/t" "$STATE/bin" "$STATE/mnt" "$STATE/ctx"
chmod 700 "$STATE/run"
echo mnt-marker >"$STATE/mnt/marker.txt"
rm -f "$STATE"/mnt/w-*
mkdir -p "$STATE/ctx/.nexus"
printf 'FROM alpine:3.21\nRUN echo hi > /x\n' >"$STATE/ctx/.nexus/Containerfile"

BASEENV=(HOME="$STATE/home" XDG_RUNTIME_DIR="$STATE/run" TMPDIR="$STATE/t"
  USER="${USER:-root}" LOGNAME="${LOGNAME:-${USER:-root}}"
  PATH="$STATE/bin:/usr/sbin:/usr/bin:/sbin:/bin")
[ -n "${SSH_AUTH_SOCK:-}" ] && BASEENV+=(SSH_AUTH_SOCK="$SSH_AUTH_SOCK")
VU=(NEXUS_NET_MODE=vhost-user)

nx() { env -i "${BASEENV[@]}" "$@"; }
gexec() { timeout "$1" env -i "${BASEENV[@]}" nexus exec "$2" -- sh -c "$3" 2>&1; }

FAILS=0
record() {
  [ "$1" = FAIL ] && FAILS=$((FAILS + 1))
  printf '%-8s %-26s %s\n' "$1" "$2" "$3"
}

HANDLES=(s9b8/main s9b8/file s9b8/snap s9b8/git s9b8/tap)
cleanup() {
  local h
  for h in "${HANDLES[@]}"; do nx nexus rm "$h" >/dev/null 2>&1; done
  nx nexus volume rm s9b8vol >/dev/null 2>&1
  [ -n "${FWDPID:-}" ] && kill "$FWDPID" 2>/dev/null
}
trap cleanup EXIT

echo "== build binaries into $STATE/bin"
(cd "$ROOT" && go build -o "$STATE/bin/nexus.new" ./cmd/nexus &&
  go build -tags s9blive -o "$STATE/bin/nexus-live.new" ./cmd/nexus) >"$STATE/build.log" 2>&1 ||
  { echo "build failed: $(tail -3 "$STATE/build.log")" >&2; exit 2; }
mv -f "$STATE/bin/nexus.new" "$STATE/bin/nexus"
mv -f "$STATE/bin/nexus-live.new" "$STATE/bin/nexus-live"
for h in "${HANDLES[@]}"; do
  if nx nexus sandbox list 2>/dev/null | grep -q "^$h "; then echo "$h already exists in $STATE; remove it first" >&2; exit 2; fi
done

net_probe() { # label handle
  local dns allow deny
  dns=$(gexec 60 "$2" "nslookup example.com 2>&1 | awk '/^Name:/{f=1} f&&/^Address/{c++} END{print c+0}'")
  allow=$(gexec 60 "$2" "wget --no-check-certificate -S -qO /dev/null -T8 https://example.com 2>&1 | head -1 | grep -c 'HTTP/1.[01] 200'")
  deny=$(gexec 60 "$2" "if wget --no-check-certificate -qO /dev/null -T8 https://google.com 2>/dev/null; then echo open; else echo blocked; fi")
  if [ "${dns:-0}" -ge 1 ] 2>/dev/null; then record PASS "$1 dns" "example.com resolved"; else record FAIL "$1 dns" "out=[$dns]"; fi
  if [ "${allow:-0}" -ge 1 ] 2>/dev/null; then record PASS "$1 egress-allow" "HTTP 200"; else record FAIL "$1 egress-allow" "out=[$allow]"; fi
  if [ "$deny" = blocked ]; then record PASS "$1 egress-deny" "google.com blocked"; else record FAIL "$1 egress-deny" "deny=[$deny]"; fi
}

echo "== main: image create under vhost-user"
if timeout 300 env -i "${BASEENV[@]}" "${VU[@]}" nexus sandbox create s9b8/main --image "$IMAGE" \
  --mount "$STATE/mnt:/mnt/host" --egress closed --repo octocat/hello-world --allow-host example.com \
  >"$STATE/create-main.log" 2>&1; then
  record PASS "create --image" "s9b8/main"
else
  record FAIL "create --image" "$(tail -3 "$STATE/create-main.log")"
fi
out=$(gexec 30 s9b8/main "echo up")
if [ "$out" = up ]; then record PASS "exec" "echo up"; else record FAIL "exec" "[$out]"; fi
net_probe "main" s9b8/main

mode=$(jq -r '.net_mode // "tap"' "$STATE"/home/.local/state/nexus/sandboxes/*/record.json 2>/dev/null | sort -u | tr '\n' ' ')
record INFO "record net_mode" "$mode"

echo "== dir mount rw"
r=$(gexec 30 s9b8/main "cat /mnt/host/marker.txt")
w=$(gexec 30 s9b8/main "touch /mnt/host/w-1 && echo rw")
if [ "$r" = mnt-marker ] && [ "$w" = rw ] && [ -e "$STATE/mnt/w-1" ]; then
  record PASS "mount rw" "read marker; guest write visible on host"
else
  record FAIL "mount rw" "r=[${r:0:100}] w=[${w:0:100}]"
fi

echo "== stop/start"
gexec 30 s9b8/main "echo persisted >/root/p; sync" >/dev/null
if timeout 120 env -i "${BASEENV[@]}" "${VU[@]}" nexus sandbox stop s9b8/main >"$STATE/stop.log" 2>&1 && timeout 240 env -i "${BASEENV[@]}" "${VU[@]}" nexus sandbox start s9b8/main >"$STATE/start.log" 2>&1; then
  out=$(gexec 60 s9b8/main "cat /root/p")
  if [ "$out" = persisted ]; then record PASS "stop/start" "state kept"; else record FAIL "stop/start" "out=[$out]"; fi
  net_probe "restarted" s9b8/main
else
  record FAIL "stop/start" "$(tail -3 "$STATE/stop.log" "$STATE/start.log" 2>/dev/null)"
fi

echo "== port forward"
gexec 30 s9b8/main '(setsid sh -c "while true; do printf \"HTTP/1.0 200 OK\r\n\r\nhello-fwd\n\" | nc -l -p 8080; done" >/dev/null 2>&1 &); sleep 1' >/dev/null
nx nexus forward s9b8/main 18988:8080 >"$STATE/fwd.log" 2>&1 &
FWDPID=$!
sleep 3
out=$(curl -sS -m10 http://127.0.0.1:18988/ 2>&1 | tr -d '\r')
kill "$FWDPID" 2>/dev/null; FWDPID=""
case "$out" in *hello-fwd*) record PASS "port forward" "hello-fwd" ;; *) record FAIL "port forward" "[${out:0:100}]" ;; esac

echo "== create --file (builder VM under vhost-user)"
if timeout 900 env -i "${BASEENV[@]}" "${VU[@]}" nexus sandbox create s9b8/file --file "$STATE/ctx" \
  --egress closed --repo octocat/hello-world --allow-host example.com >"$STATE/create-file.log" 2>&1; then
  out=$(gexec 30 s9b8/file "cat /x")
  if [ "$out" = hi ]; then record PASS "create --file" "cat /x = hi"; else record FAIL "create --file" "cat /x=[$out]"; fi
else
  record FAIL "create --file" "$(tail -3 "$STATE/create-file.log")"
fi

echo "== git-ssh relay"
if [ -z "${SSH_AUTH_SOCK:-}" ]; then
  record SKIP "git-ssh clone" "SSH_AUTH_SOCK unset"
elif timeout 600 env -i "${BASEENV[@]}" "${VU[@]}" nexus sandbox create s9b8/git --image "$GIT_IMAGE" \
  --egress closed --repo octocat/hello-world --allow-host example.com \
  --egress-policy-json '{"":{"github.com":{"Paths":["/octocat/Hello-World/**"]}}}' >"$STATE/create-git.log" 2>&1; then
  out=$(gexec 120 s9b8/git "GIT_SSH_COMMAND=/sbin/nexus-agent\ git-ssh git clone -q git@github.com:octocat/Hello-World.git /tmp/hw && git -C /tmp/hw rev-parse --short HEAD")
  case "$out" in *7fd1a60*) record PASS "git-ssh clone" "HEAD 7fd1a60" ;; *) record FAIL "git-ssh clone" "[${out:0:160}]" ;; esac
else
  record FAIL "git-ssh clone" "create: $(tail -3 "$STATE/create-git.log")"
fi

echo "== snapshot / restore"
if timeout 300 env -i "${BASEENV[@]}" "${VU[@]}" nexus sandbox create s9b8/snap --image "$IMAGE" --memory 512 --memory-max 1024 \
  --egress closed --repo octocat/hello-world --allow-host example.com >"$STATE/create-snap.log" 2>&1; then
  gexec 30 s9b8/snap "echo snap-marker >/root/m; sync" >/dev/null
  live() { timeout 300 env -i "${BASEENV[@]}" "${VU[@]}" nexus-live s9b-live "$@" 2>>"$STATE/live.err"; }
  out=$(live snapshot s9b8/snap); sid=$(awk '/^snapshot /{print $2}' <<<"$out")
  if [ -n "$sid" ]; then
    out=$(live restore "$sid" 1)
    cid=$(awk '/^child /{print $2}' <<<"$out" | head -1)
    chandle=$(awk '/^child /{print $3}' <<<"$out" | head -1)
    if [ -n "$cid" ]; then
      HANDLES+=("$chandle")
      ok=""
      for _ in $(seq 1 60); do ok=$(gexec 10 "$cid" "cat /root/m") && [ "$ok" = snap-marker ] && break; sleep 1; done
      if [ "$ok" = snap-marker ]; then record PASS "snapshot/restore" "child $chandle kept marker"; else record FAIL "snapshot/restore" "marker=[$ok]"; fi
    else
      record FAIL "snapshot/restore" "restore out=[${out:0:160}] err=[$(tail -2 "$STATE/live.err")]"
    fi
    live snaprm "$sid" >/dev/null
  else
    record FAIL "snapshot/restore" "snapshot out=[${out:0:160}] err=[$(tail -2 "$STATE/live.err")]"
  fi
else
  record FAIL "snapshot/restore" "create: $(tail -3 "$STATE/create-snap.log")"
fi

echo "== tap-mode create (expected to FAIL when restricted)"
if timeout 300 env -i "${BASEENV[@]}" nexus sandbox create s9b8/tap --image "$IMAGE" \
  --egress closed --repo octocat/hello-world --allow-host example.com >"$STATE/create-tap.log" 2>&1; then
  record NOTRESTR "tap create" "succeeded: host is not restricted"
elif grep -q 'NEXUS_NET_MODE=vhost-user' "$STATE/create-tap.log"; then
  record PASS "tap create fails" "message names NEXUS_NET_MODE=vhost-user"
else
  record FAIL "tap create fails" "no fix named: $(tail -3 "$STATE/create-tap.log")"
fi

echo "== $FAILS failure(s); cleanup by exact name on exit"
[ "$FAILS" = 0 ]
