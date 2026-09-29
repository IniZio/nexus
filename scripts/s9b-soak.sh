#!/usr/bin/env bash
# S9b-7 soak: one vhost-user sandbox, periodic egress probes. Design: doc/design/s9b7-parity.md
set -u

BIN=""
STATE=/var/tmp/s9bsoak
DURATION=7200
INTERVAL=30
IMAGE=alpine:3.21
HANDLE=s9bsoak/vu
ALLOW=example.com
DENY=google.com

while [ $# -gt 0 ]; do
  case "$1" in
    --bin) BIN=$2; shift 2 ;;
    --state) STATE=$2; shift 2 ;;
    --duration) DURATION=$2; shift 2 ;;
    --interval) INTERVAL=$2; shift 2 ;;
    --image) IMAGE=$2; shift 2 ;;
    --handle) HANDLE=$2; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done
if ! [ -x "$BIN" ]; then
  echo "usage: $0 --bin NEXUS [--state DIR] [--duration SECS] [--interval SECS] [--image REF] [--handle NS/vu]" >&2
  exit 2
fi

BIN=$(readlink -f "$BIN")
mkdir -p "$STATE/home" "$STATE/run" "$STATE/t" "$STATE/bin"
chmod 700 "$STATE/run"
cp "$BIN" "$STATE/bin/nexus.new" && mv -f "$STATE/bin/nexus.new" "$STATE/bin/nexus"
LOG="$STATE/soak.log"

NXBASE=(HOME="$STATE/home" XDG_RUNTIME_DIR="$STATE/run" TMPDIR="$STATE/t"
  USER="${USER:-root}" LOGNAME="${LOGNAME:-${USER:-root}}"
  PATH="$STATE/bin:/usr/sbin:/usr/bin:/sbin:/bin")
nx() { env -i "${NXBASE[@]}" nexus "$@"; }
log() { printf '%s %s\n' "$(date -u +%FT%TZ)" "$*" >>"$LOG"; }
# gexec HANDLE TIMEOUT CMD
gexec() { timeout "$2" env -i "${NXBASE[@]}" nexus exec "$1" -- sh -c "$3" 2>&1; }

cleanup() {
  nx rm "$HANDLE" >/dev/null 2>&1
  log "cleanup: removed $HANDLE"
}
trap cleanup EXIT

# mk_sandbox HANDLE
mk_sandbox() {
  local h=$1 id got
  if nx sandbox list 2>/dev/null | grep -q "^$h "; then
    log "FATAL $h already exists in $STATE"
    return 2
  fi
  if ! timeout 600 env -i "${NXBASE[@]}" nexus sandbox create "$h" --image "$IMAGE" --egress closed --repo octocat/hello-world --allow-host "$ALLOW" >>"$LOG" 2>&1; then
    log "FATAL create $h failed"
    return 1
  fi
  id=$(nx sandbox list | awk -v h="$h" '$1==h{print $NF}')
  got=$(jq -r '.vhost_socket // ""' "$STATE/home/.local/state/nexus/sandboxes/$id/record.json" 2>/dev/null)
  log "created $h id=$id vhost_socket=${got:-none}"
  if [ -z "$got" ]; then
    log "FATAL $h has no vhost_socket"
    return 1
  fi
  return 0
}

# probe HANDLE ; sets global fails, probes
probe() {
  local h=$1 dns allow deny bad="" rawdns rawallow hostdns
  dns=$(gexec "$h" 60 "nslookup $ALLOW 2>&1 | awk '/^Address/{c++} END{print c+0}'")
  allow=$(gexec "$h" 60 "wget --no-check-certificate -S -qO /dev/null -T8 https://$ALLOW 2>&1 | head -1 | grep -c 'HTTP/1.[01] 200'")
  deny=$(gexec "$h" 60 "if wget --no-check-certificate -qO /dev/null -T8 https://$DENY 2>/dev/null; then echo open; else echo blocked; fi")
  [ "${dns:-0}" -ge 2 ] 2>/dev/null || bad="$bad dns=[$dns]"
  [ "${allow:-0}" -ge 1 ] 2>/dev/null || bad="$bad allow=[$allow]"
  [ "$deny" = blocked ] || bad="$bad deny=[$deny]"
  if [ -n "$bad" ]; then
    fails=$((fails + 1))
    log "probe $probes FAIL$bad"
    rawdns=$(gexec "$h" 60 "nslookup $ALLOW" | head -c 300 | tr '\n' '|')
    rawallow=$(gexec "$h" 60 "wget --no-check-certificate -S -qO /dev/null -T8 https://$ALLOW" | head -c 300 | tr '\n' '|')
    hostdns=$(getent hosts "$ALLOW" 2>&1 | tr '\n' '|')
    log "probe $probes raw nslookup: $rawdns"
    log "probe $probes raw wget: $rawallow"
    log "probe $probes host getent hosts $ALLOW: ${hostdns:-<none>}"
  else
    log "probe $probes ok dns=$dns allow=200 deny=blocked"
  fi
}

: >"$LOG"
log "start duration=${DURATION}s interval=${INTERVAL}s image=$IMAGE state=$STATE"
mk_sandbox "$HANDLE" || exit $?

probes=0
fails=0
end=$(($(date +%s) + DURATION))
while [ "$(date +%s)" -lt "$end" ]; do
  probes=$((probes + 1))
  probe "$HANDLE"
  sleep "$INTERVAL"
done

errs=$(gexec "$HANDLE" 60 "dmesg | grep -iE 'virtio.*(error|fail|reset)|eth0.*(error|reset|timeout)|NETDEV WATCHDOG|Call Trace'")
if [ -n "$errs" ]; then
  fails=$((fails + 1))
  log "dmesg FAIL: $(printf '%s' "$errs" | head -5 | tr '\n' '|')"
else
  log "dmesg clean: no virtio/net errors"
fi
if [ "$fails" -eq 0 ]; then
  log "RESULT PASS probes=$probes"
else
  log "RESULT FAIL probes=$probes failures=$fails"
  exit 1
fi
