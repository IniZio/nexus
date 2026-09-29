#!/usr/bin/env bash
# S9b-R: upgrade-survival regression harness.
# NEXUS_NET_MODE=vhost-user exported. NEXUS_NET_MODE is a create-time input

# Usage and design: doc/design/s9b-regression.md
set -u

OLD=""
NEW=""
STATE=/var/tmp/s9br
IMAGE=alpine:3.21
KEEP=0
HANDLE=s9br/fixture
SNAPH=s9br/snapfix
VOL=s9brvol
ROOT=$(cd "$(dirname "$0")/.." && pwd)

while [ $# -gt 0 ]; do
  case "$1" in
    --old) OLD=$2; shift 2 ;;
    --new) NEW=$2; shift 2 ;;
    --state) STATE=$2; shift 2 ;;
    --image) IMAGE=$2; shift 2 ;;
    --keep) KEEP=1; shift ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done
if ! { [ -x "$OLD" ] && [ -x "$NEW" ]; }; then echo "usage: $0 --old BIN --new BIN [--state DIR] [--image REF] [--keep]" >&2; exit 2; fi
command -v jq >/dev/null || { echo "jq required" >&2; exit 2; }

OLD=$(readlink -f "$OLD")
NEW=$(readlink -f "$NEW")
mkdir -p "$STATE/home" "$STATE/run" "$STATE/t" "$STATE/mnt" "$STATE/bin" "$STATE/rec"
chmod 700 "$STATE/run"
echo mnt-marker >"$STATE/mnt/marker.txt"
rm -f "$STATE"/mnt/w-*

BASEENV=(HOME="$STATE/home" XDG_RUNTIME_DIR="$STATE/run" TMPDIR="$STATE/t"
  USER="${USER:-root}" LOGNAME="${LOGNAME:-${USER:-root}}"
  PATH="$STATE/bin:/usr/sbin:/usr/bin:/sbin:/bin")
EXTRA=()
nx() { env -i "${BASEENV[@]}" "${EXTRA[@]}" nexus "$@"; }
REC_GLOB="$STATE/home/.local/state/nexus/sandboxes"

declare -a RESULTS=()
FAILS=0
record() { # status label evidence
  RESULTS+=("$1|$2|$3")
  [ "$1" = FAIL ] && FAILS=$((FAILS + 1))
  printf '%-5s %-34s %s\n' "$1" "$2" "$3"
}

ID=""
recfile() { echo "$REC_GLOB/$ID/record.json"; }

cleanup() {
  EXTRA=()
  nx rm "$HANDLE" >/dev/null 2>&1
  nx rm "$SNAPH" >/dev/null 2>&1
  nx volume rm "$VOL" >/dev/null 2>&1
  if [ "$KEEP" = 0 ] && [ -n "$ID" ]; then
    for p in $(pgrep -f "$ID" 2>/dev/null); do
      if tr '\0' ' ' <"/proc/$p/cmdline" 2>/dev/null | grep -q "$ID"; then
        echo "WARN: leftover process $p for $ID after rm" >&2
      fi
    done
  fi
}
trap cleanup EXIT

# Fields that legitimately change across supervisor/VM lifecycle.
LIFECYCLE='["state","instance_id","supervisor_pid","supervisor_sock","netns_child_pid","netns_child_pgid","netns_child_start_time","guest_tap_name","ch_api_socket","netns_control_socket","netns_control_token","stop_reason","updated_at"]'

check_record() {
  local label=$1 prev=$2 cur changed bad
  cur=$(recfile)
  if jq -e 'has("net_mode")' "$cur" >/dev/null; then
    record FAIL "$label record" "net_mode written on legacy record: $(jq -c '.net_mode' "$cur")"
    return
  fi
  bad=$(jq -n --slurpfile a "$prev" --slurpfile b "$cur" --argjson ok "$LIFECYCLE" '
    ($a[0] | keys) as $ka | ($b[0] | keys) as $kb
    | [($ka + $kb | unique)[] | select(($a[0][.]) != ($b[0][.])) | select(. as $k | $ok | index($k) | not)]')
  changed=$(jq -n --slurpfile a "$prev" --slurpfile b "$cur" '[($a[0]|keys)+($b[0]|keys)|unique[]|select(($a[0][.])!=($b[0][.]))]|join(",")' -r)
  if [ "$bad" != "[]" ]; then
    record FAIL "$label record" "non-lifecycle fields changed: $bad"
    return
  fi
  if [ "$(jq -r '.state' "$cur")" = running ] && [ -z "$(jq -r '.guest_tap_name // ""' "$cur")" ]; then
    record FAIL "$label record" "running but guest_tap_name empty"
    return
  fi
  record PASS "$label record" "net_mode absent; changed=[${changed}] all lifecycle; tap=$(jq -r '.guest_tap_name // "-"' "$cur")"
}

check_tap() {
  local label=$1 cpid tap bad=""
  tap=$(jq -r '.guest_tap_name // ""' "$(recfile)")
  cpid=$(jq -r '.netns_child_pid // 0' "$(recfile)")
  if ! grep -q "^ *$tap:" "/proc/$cpid/net/dev" 2>/dev/null; then
    bad="tap $tap not in netns of pid $cpid"
  fi
  local spid
  spid=$(jq -r '.supervisor_pid // 0' "$(recfile)")
  if tr '\0' ' ' <"/proc/$spid/cmdline" 2>/dev/null | grep -q -- '--net-mode'; then
    bad="$bad; supervisor argv carries --net-mode: $(tr '\0' ' ' <"/proc/$spid/cmdline" | grep -o -- '--net-mode [a-z-]*')"
  fi
  if [ -n "$bad" ]; then
    record FAIL "$label tap" "$bad"
  else
    record PASS "$label tap" "$tap present in /proc/$cpid/net/dev; no --net-mode in supervisor argv"
  fi
}

gexec() { timeout "$1" env -i "${BASEENV[@]}" "${EXTRA[@]}" nexus exec "${TARGET:-$HANDLE}" -- sh -c "$2" 2>&1; }
live() { timeout 300 env -i "${BASEENV[@]}" "${EXTRA[@]}" nexus-live s9b-live "$@" 2>>"$STATE/live.err"; }
rec_of() { echo "$REC_GLOB/$1/record.json"; }

probe() {
  local label=$1 n=$2 dns allow deny vol volw mntr mntw
  dns=$(gexec 60 "nslookup example.com 2>&1 | awk '/^Name:/{f=1} f&&/^Address/{c++} END{print c+0}'")
  allow=$(gexec 60 "wget --no-check-certificate -S -qO /dev/null -T8 https://example.com 2>&1 | head -1 | grep -c 'HTTP/1.[01] 200'")
  deny=$(gexec 60 "if wget --no-check-certificate -qO /dev/null -T8 https://google.com 2>/dev/null; then echo open; else echo blocked; fi")
  vol=$(gexec 30 "cat /data/x")
  volw=$(gexec 30 "echo step-$n >/data/y && cat /data/y")
  mntr=$(gexec 30 "cat /mnt/host/marker.txt")
  mntw=$(gexec 30 "touch /mnt/host/w-$n && echo rw")
  if [ "${dns:-0}" -ge 1 ] 2>/dev/null; then record PASS "$label dns" "example.com resolved ($dns addr)"; else record FAIL "$label dns" "out=[$dns]"; fi
  if [ "${allow:-0}" -ge 1 ] 2>/dev/null; then record PASS "$label egress-allow" "https://example.com HTTP 200"; else record FAIL "$label egress-allow" "out=[$allow]"; fi
  if [ "$deny" = blocked ]; then record PASS "$label egress-deny" "https://google.com blocked"; else record FAIL "$label egress-deny" "deny=[$deny]"; fi
  if [ "$vol" = volfile ] && [ "$volw" = "step-$n" ]; then record PASS "$label volume" "/data/x=volfile intact; /data/y=$volw"; else record FAIL "$label volume" "vol=[${vol:0:120}] volw=[${volw:0:120}]"; fi
  if [ "$mntr" = mnt-marker ] && [ "$mntw" = rw ] && [ -e "$STATE/mnt/w-$n" ]; then
    record PASS "$label mount-rw" "marker read; guest write w-$n visible on host"
  else
    record FAIL "$label mount-rw" "mntr=[${mntr:0:120}] mntw=[${mntw:0:120}] host_has_w-$n=$([ -e "$STATE/mnt/w-$n" ] && echo y || echo n)"
  fi
}

probe_net() { # label handle: DNS + allowed + denied egress only
  local label=$1 dns allow deny
  TARGET=$2
  dns=$(gexec 60 "nslookup example.com 2>&1 | awk '/^Name:/{f=1} f&&/^Address/{c++} END{print c+0}'")
  allow=$(gexec 60 "wget --no-check-certificate -S -qO /dev/null -T8 https://example.com 2>&1 | head -1 | grep -c 'HTTP/1.[01] 200'")
  deny=$(gexec 60 "if wget --no-check-certificate -qO /dev/null -T8 https://google.com 2>/dev/null; then echo open; else echo blocked; fi")
  TARGET=
  if [ "${dns:-0}" -ge 1 ] 2>/dev/null; then record PASS "$label dns" "example.com resolved ($dns addr)"; else record FAIL "$label dns" "out=[$dns]"; fi
  if [ "${allow:-0}" -ge 1 ] 2>/dev/null; then record PASS "$label egress-allow" "https://example.com HTTP 200"; else record FAIL "$label egress-allow" "out=[$allow]"; fi
  if [ "$deny" = blocked ]; then record PASS "$label egress-deny" "https://google.com blocked"; else record FAIL "$label egress-deny" "deny=[$deny]"; fi
}

check_child_tap() { # label child-id: tap, net_mode absent, no --net-mode argv, guest state survived
  local label=$1 cid=$2 rf cpid tap spid bad="" marker
  rf=$(rec_of "$cid")
  tap=$(jq -r '.guest_tap_name // ""' "$rf")
  cpid=$(jq -r '.netns_child_pid // 0' "$rf")
  spid=$(jq -r '.supervisor_pid // 0' "$rf")
  jq -e 'has("net_mode")' "$rf" >/dev/null && bad="net_mode written: $(jq -c .net_mode "$rf")"
  jq -e 'has("vhost_socket") and (.vhost_socket != null and .vhost_socket != "")' "$rf" >/dev/null && bad="$bad; vhost_socket set"
  [ -n "$tap" ] || bad="$bad; guest_tap_name empty"
  grep -q "^ *$tap:" "/proc/$cpid/net/dev" 2>/dev/null || bad="$bad; tap $tap not in netns of pid $cpid"
  if [ "$spid" -gt 0 ] && tr '\0' ' ' <"/proc/$spid/cmdline" 2>/dev/null | grep -q -- '--net-mode'; then bad="$bad; supervisor argv carries --net-mode"; fi
  TARGET=$cid marker=$(gexec 30 "cat /root/s9b-marker")
  TARGET=
  [ "$marker" = snap-marker ] || bad="$bad; guest marker [${marker:0:60}]"
  if [ -z "$bad" ]; then
    record PASS "$label tap" "$tap in /proc/$cpid/net/dev; net_mode absent; no vhost_socket; marker survived"
  else
    record FAIL "$label tap" "$bad"
  fi
}

wait_child() { # child-id: wait until exec answers
  for _ in $(seq 1 60); do
    if TARGET=$1 gexec 10 true >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  return 1
}

snap_record() { cp "$(recfile)" "$STATE/rec/$1.json"; }

wait_running() { # wait until record state=running and exec answers
  for _ in $(seq 1 60); do
    if [ "$(jq -r '.state' "$(recfile)" 2>/dev/null)" = running ] && nx exec "$HANDLE" -- true >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  return 1
}

echo "== fixture: create with OLD binary ($OLD)"
cp "$OLD" "$STATE/bin/nexus.new" && mv -f "$STATE/bin/nexus.new" "$STATE/bin/nexus"
EXTRA=()
if nx sandbox list 2>/dev/null | grep -q "^$HANDLE "; then echo "fixture $HANDLE already exists in $STATE; remove it first" >&2; exit 2; fi
nx volume create "$VOL" >/dev/null 2>&1
timeout 300 env -i "${BASEENV[@]}" nexus sandbox create "$HANDLE" --image "$IMAGE" \
  --mount-named "$VOL:/data" --mount "$STATE/mnt:/mnt/host" \
  --egress closed --repo octocat/hello-world --allow-host example.com >"$STATE/create.log" 2>&1 \
  || { record FAIL "fixture create" "$(tail -3 "$STATE/create.log")"; exit 1; }
ID=$(nx sandbox list | awk -v h="$HANDLE" '$1==h{print $NF}')
[ -n "$ID" ] || { record FAIL "fixture create" "no id in list"; exit 1; }

if jq -e '.envelope.open_egress // false | not' "$(recfile)" >/dev/null &&
  [ "$(jq -r '.envelope.AllowedHosts | join(",")' "$(recfile)")" = example.com ] &&
  [ -n "$(jq -r '.guest_tap_name // ""' "$(recfile)")" ] && ! jq -e 'has("net_mode")' "$(recfile)" >/dev/null; then
  record PASS "fixture record" "tap ($(jq -r .guest_tap_name "$(recfile)")), open_egress false, allow=[example.com], net_mode absent"
else
  record FAIL "fixture record" "$(jq -c '{envelope,guest_tap_name,net_mode}' "$(recfile)")"
  exit 1
fi
timeout 60 env -i "${BASEENV[@]}" nexus exec "$HANDLE" -- sh -c 'echo volfile >/data/x; sync' >/dev/null 2>&1
snap_record 00-created
probe "pre-upgrade" 0

EXTRA=()
if timeout 300 env -i "${BASEENV[@]}" nexus sandbox create "$SNAPH" --image "$IMAGE" --memory 512 --memory-max 1024 \
  --egress closed --repo octocat/hello-world --allow-host example.com >"$STATE/create2.log" 2>&1 &&
  timeout 60 env -i "${BASEENV[@]}" nexus exec "$SNAPH" -- sh -c 'echo snap-marker >/root/s9b-marker; sync' >/dev/null 2>&1; then
  SNAPID=$(nx sandbox list | awk -v h="$SNAPH" '$1==h{print $NF}')
  record PASS "snapfix create" "tap fixture without mounts/volume: $SNAPH ($SNAPID)"
else
  record FAIL "snapfix create" "$(tail -3 "$STATE/create2.log")"
  SNAPID=""
fi

echo "== atomic-rename upgrade to NEW binary ($NEW); NEXUS_NET_MODE=vhost-user from here on"
cp "$NEW" "$STATE/bin/nexus.new" && mv -f "$STATE/bin/nexus.new" "$STATE/bin/nexus"
EXTRA=(NEXUS_NET_MODE=vhost-user)

probe "(a) running" 1
snap_record 01-running
check_record "(a) running" "$STATE/rec/00-created.json"
check_tap "(a) running"

oldpid=$(jq -r .supervisor_pid "$(recfile)")
if out=$(timeout 180 env -i "${BASEENV[@]}" "${EXTRA[@]}" nexus supervisor-upgrade "$HANDLE" 2>&1); then
  newpid=$(jq -r .supervisor_pid "$(recfile)")
  if [ "$newpid" != "$oldpid" ]; then
    record PASS "(b) supervisor-upgrade" "pid $oldpid -> $newpid; $(tail -1 <<<"$out" | cut -c1-90)"
  else
    record FAIL "(b) supervisor-upgrade" "pid unchanged $oldpid: $out"
  fi
else
  record FAIL "(b) supervisor-upgrade" "$(tail -3 <<<"$out")"
fi
probe "(b) adopt" 2
snap_record 02-adopt
check_record "(b) adopt" "$STATE/rec/01-running.json"
check_tap "(b) adopt"

timeout 120 env -i "${BASEENV[@]}" "${EXTRA[@]}" nexus sandbox stop "$HANDLE" >/dev/null 2>&1
snap_record 03-stopped
check_record "(c) stopped" "$STATE/rec/02-adopt.json"
if timeout 240 env -i "${BASEENV[@]}" "${EXTRA[@]}" nexus sandbox start "$HANDLE" >"$STATE/start.log" 2>&1 && wait_running; then
  record PASS "(c) start" "running again under NEW binary"
else
  record FAIL "(c) start" "$(tail -3 "$STATE/start.log")"
fi
probe "(c) stop-start" 3
snap_record 04-restarted
check_record "(c) restarted" "$STATE/rec/03-stopped.json"
check_tap "(c) restarted"

(cd "$ROOT" && go build -tags s9blive -o "$STATE/bin/nexus-live.new" ./cmd/nexus) >"$STATE/live-build.log" 2>&1 &&
  mv -f "$STATE/bin/nexus-live.new" "$STATE/bin/nexus-live"
EXTRA=(NEXUS_NET_MODE=vhost-user)
if [ -x "$STATE/bin/nexus-live" ] && [ -n "$SNAPID" ]; then
  if out=$(live snapshot "$HANDLE"); then
    record FAIL "(d) snapshot mount guard" "snapshot of mount+volume fixture unexpectedly succeeded: $out"
  elif grep -q 'live host-directory mount' "$STATE/live.err" || grep -q 'named volume' "$STATE/live.err"; then
    record PASS "(d) snapshot mount guard" "$HANDLE refused: $(grep -o 'live host-directory mount(s)\|named volume(s)' "$STATE/live.err" | tail -1)"
  else
    record FAIL "(d) snapshot mount guard" "no guard message: $(tail -2 "$STATE/live.err")"
  fi
  : >"$STATE/live.err"

  t0=$(date +%s.%N)
  out=$(live snapshot "$SNAPH"); sid=$(awk '/^snapshot /{print $2}' <<<"$out")
  t1=$(date +%s.%N)
  if [ -n "$sid" ]; then
    record PASS "(d) snapshot" "$SNAPH -> $sid in $(awk -v a="$t0" -v b="$t1" 'BEGIN{printf "%.1fs", b-a}'), $(du -sm "$STATE/home/.local/state/nexus/snapshots/$sid" | cut -f1) MiB on disk"
    out=$(live restore "$sid" 1)
    cid=$(awk '/^child /{print $2}' <<<"$out" | head -1)
    chandle=$(awk '/^child /{print $3}' <<<"$out" | head -1)
    if [ -n "$cid" ] && wait_child "$cid"; then
      record PASS "(d) restore" "child $chandle running under NEW binary, NEXUS_NET_MODE=vhost-user exported"
      check_child_tap "(d) restored" "$cid"
      probe_net "(d) restored" "$cid"
    else
      record FAIL "(d) restore" "out=[${out:0:200}] err=[$(tail -2 "$STATE/live.err")]"
    fi
    [ -n "$chandle" ] && nx rm "$chandle" >/dev/null 2>&1
    live snaprm "$sid" >/dev/null
  else
    record FAIL "(d) snapshot" "out=[${out:0:200}] err=[$(tail -2 "$STATE/live.err")]"
  fi

  snapdir="$STATE/home/.local/state/nexus/snapshots"
  before=$(ls "$snapdir" 2>/dev/null | grep -v '\.')
  out=$(live fork "$SNAPH" 2)
  mapfile -t kids < <(awk '/^child /{print $2 " " $3}' <<<"$out")
  if [ "${#kids[@]}" = 2 ]; then
    taps=""
    for k in "${kids[@]}"; do
      cid=${k% *}; chandle=${k#* }
      if wait_child "$cid"; then
        check_child_tap "(e) fork child ${cid: -6}" "$cid"
        probe_net "(e) fork child ${cid: -6}" "$cid"
        taps="$taps $(jq -r .netns_child_pid "$(rec_of "$cid")")"
      else
        record FAIL "(e) fork child ${cid: -6}" "not answering exec"
      fi
    done
    if [ "$(tr ' ' '\n' <<<"$taps" | sort -u | grep -c .)" = 2 ]; then
      record PASS "(e) fork distinct netns" "own netns child pids:$taps (tap names may repeat: one tap per netns)"
    else
      record FAIL "(e) fork distinct netns" "netns child pids=[$taps]"
    fi
    for k in "${kids[@]}"; do nx rm "${k#* }" >/dev/null 2>&1; done
  else
    record FAIL "(e) fork" "out=[${out:0:200}] err=[$(grep -v INFO "$STATE/live.err" | tail -2 | cut -c1-300)]"
  fi
  for leaked in $(comm -13 <(sort <<<"$before") <(ls "$snapdir" 2>/dev/null | grep -v '\.' | sort)); do live snaprm "$leaked" >/dev/null; done
else
  record FAIL "(d,e) snapshot/fork" "nexus-live missing ($(tail -2 "$STATE/live-build.log" 2>/dev/null)) or no snapfix"
fi
EXTRA=(NEXUS_NET_MODE=vhost-user)

spid=$(jq -r .supervisor_pid "$(recfile)")
if tr '\0' ' ' <"/proc/$spid/cmdline" 2>/dev/null | grep -q "__supervisor.*$ID"; then
  kill -9 "$spid"
  sleep 2
  before=$(grep -c 'supervisor.reacquire.acquired' "$STATE/home/.local/state/nexus/supervisors/$ID/supervisor.log" 2>/dev/null)
  rec=$(timeout 180 env -i "${BASEENV[@]}" "${EXTRA[@]}" nexus recover 2>&1)
  wait_running
  npid=$(jq -r .supervisor_pid "$(recfile)")
  after=$(grep -c 'supervisor.reacquire.acquired' "$STATE/home/.local/state/nexus/supervisors/$ID/supervisor.log" 2>/dev/null)
  if [ "$npid" != "$spid" ] && kill -0 "$npid" 2>/dev/null && [ "$after" -gt "$before" ] &&
    grep -q "rebuilt the perimeter" <<<"$rec"; then
    record PASS "(f) SIGKILL supervisor" "killed $spid; replacement $npid; reacquire.acquired $before->$after; perimeter rebuilt"
  else
    record FAIL "(f) SIGKILL supervisor" "killed=$spid new=$npid reacquire $before->$after recover=[$rec]"
  fi
else
  record FAIL "(f) SIGKILL supervisor" "pid $spid cmdline does not contain fixture id; NOT killing"
fi
probe "(f) post-kill" 5
check_record "(f) post-kill" "$STATE/rec/04-restarted.json"
check_tap "(f) post-kill"

check_record "overall(created->final)" "$STATE/rec/00-created.json"

echo
echo "== SUMMARY"
for r in "${RESULTS[@]}"; do IFS='|' read -r s l e <<<"$r"; printf '%-5s %-34s %s\n' "$s" "$l" "$e"; done
echo "fails=$FAILS"
[ "$FAILS" = 0 ]
