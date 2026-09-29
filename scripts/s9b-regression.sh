#!/usr/bin/env bash
# S9b-R: upgrade-survival regression harness.
#
# Proves a tap-mode sandbox created by an OLD nexus binary keeps working (and
# stays tap, with its record.json untouched except lifecycle fields) after the
# install is atomically swapped to a NEW binary, even with
# NEXUS_NET_MODE=vhost-user exported. NEXUS_NET_MODE is a create-time input
# only (plan invariant); every later lifecycle op must use the recorded mode.
#
# Usage:
#   scripts/s9b-regression.sh --old /path/nexus-old --new /path/nexus-new \
#       [--state /var/tmp/s9br] [--image alpine:3.21] [--keep]
#
# Build the binaries first (see doc/design/s9b-regression.md); never use bare
# `go test ./...` in this repo.
#
# Isolation: HOME, XDG_RUNTIME_DIR and TMPDIR live under --state; the
# environment is scrubbed (env -i). Only the sandbox s9br/fixture and volume
# s9brvol created here are ever removed, by exact name. Processes are only
# signalled after their cmdline is verified to contain this fixture's id.
#
# Exit status: 0 iff no step FAILed (SKIPs are reported, not failures).

set -u

OLD=""
NEW=""
STATE=/var/tmp/s9br
IMAGE=alpine:3.21
KEEP=0
HANDLE=s9br/fixture
VOL=s9brvol

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

# check_record <label> <prev-snapshot> : AC4 assertions on the current record.
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

# check_tap <label> : tap device lives in the sandbox netns; supervisor argv has no --net-mode.
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

# gexec <seconds> <shell-snippet> : run a snippet in the guest with a hard cap.
gexec() { timeout "$1" env -i "${BASEENV[@]}" "${EXTRA[@]}" nexus exec "$HANDLE" -- sh -c "$2" 2>&1; }

# probe <label> <n> : DNS, allowed/denied egress, volume, rw mount. Each check is
# its own capped exec so a hang is attributed to the exact operation.
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

snap_record() { cp "$(recfile)" "$STATE/rec/$1.json"; }

wait_running() { # wait until record state=running and exec answers
  for _ in $(seq 1 60); do
    if [ "$(jq -r '.state' "$(recfile)" 2>/dev/null)" = running ] && nx exec "$HANDLE" -- true >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  return 1
}

# ---------------------------------------------------------------- fixture
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

# ---------------------------------------------------------------- upgrade
echo "== atomic-rename upgrade to NEW binary ($NEW); NEXUS_NET_MODE=vhost-user from here on"
cp "$NEW" "$STATE/bin/nexus.new" && mv -f "$STATE/bin/nexus.new" "$STATE/bin/nexus"
EXTRA=(NEXUS_NET_MODE=vhost-user)

# (a) running supervisor (old binary process) under new CLI.
probe "(a) running" 1
snap_record 01-running
check_record "(a) running" "$STATE/rec/00-created.json"
check_tap "(a) running"

# (b) supervisor-upgrade (adopt).
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

# (c) stop -> start.
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

# (d)/(e) snapshot/restore/fork: only testable if a CLI verb exists.
HAVE_SNAP=0
for bin in "$OLD" "$NEW"; do
  verbs=$("$bin" --help 2>&1 | awk '/^Commands:/{f=1;next} /^Flags:/{f=0} f{print $1}')
  sub=$(env -i "${BASEENV[@]}" "$bin" sandbox nosuchverb 2>&1 | sed -n 's/.*valid: //p')
  if grep -Eqw 'snapshot|fork|clone' <<<"$verbs $sub"; then
    HAVE_SNAP=1
    record FAIL "(d,e) snapshot/fork" "verb exists in $(basename "$bin"); extend harness: $verbs $sub"
  fi
done
if [ "$HAVE_SNAP" = 0 ]; then
  record SKIP "(d) snapshot/restore" "no CLI verb in OLD or NEW (sandbox verbs: create list rm start stop); service-level only"
  record SKIP "(e) fork" "no CLI verb in OLD or NEW; service-level only"
fi

# (f) SIGKILL this fixture's supervisor; replacement must reacquire perimeter.
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

# AC4 overall: created record vs final record.
check_record "overall(created->final)" "$STATE/rec/00-created.json"

# ---------------------------------------------------------------- report
echo
echo "== SUMMARY"
for r in "${RESULTS[@]}"; do IFS='|' read -r s l e <<<"$r"; printf '%-5s %-34s %s\n' "$s" "$l" "$e"; done
echo "fails=$FAILS"
[ "$FAILS" = 0 ]
