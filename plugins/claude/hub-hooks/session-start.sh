#!/usr/bin/env bash
# Session hub registration. Silent no-op on any failure.
exec 2>/dev/null
[ "${NEXUS_HUB_DELIVERY:-}" = mod ] && exit 0
command -v nexus >/dev/null || exit 0
command -v timeout >/dev/null || exit 0
command -v jq >/dev/null || command -v python3 >/dev/null || exit 0
emit() { # $1=event $2=preamble $3=tag attrs $4=body
  if command -v jq >/dev/null; then
    jq -n --arg e "$1" --arg p "$2" --arg a "$3" --arg b "$4" \
      '{hookSpecificOutput:{hookEventName:$e,additionalContext:"\($p)\n<nexus-hub-event\($a) not-user-input>\n\($b | gsub("<";"‹"))\n</nexus-hub-event>"}}'
  else
    python3 -c 'import json,sys
e,p,a,b=sys.argv[1:5]
print(json.dumps({"hookSpecificOutput":{"hookEventName":e,"additionalContext":p+"\n<nexus-hub-event"+a+" not-user-input>\n"+b.replace("<","\u2039")+"\n</nexus-hub-event>"}}))' "$1" "$2" "$3" "$4"
  fi
}
nexus hub 2>&1 | grep -q hello || exit 0

# claude pid: nearest ancestor whose comm is claude, else direct parent.
pid=$PPID
p=$PPID
for _ in 1 2 3 4 5 6; do
  [ -n "$p" ] && [ "$p" -gt 1 ] 2>/dev/null || break
  c=$(ps -o comm= -p "$p" 2>/dev/null | tr -d ' ')
  case "$c" in claude*) pid=$p; break ;; esac
  p=$(ps -o ppid= -p "$p" 2>/dev/null | tr -d ' ')
done

args=(--pid "$pid" --kind claude --agent claude --cwd "${CLAUDE_PROJECT_DIR:-$PWD}" --shell)
out=$(timeout 3 nexus hub hello "${args[@]}") || exit 0
[ -n "$out" ] || exit 0

# Parse, never eval: expect `export NEXUS_HUB_SESSION='v' NEXUS_HUB_SEAT='v'`.
sess=; seat=
for tok in $out; do
  case "$tok" in
    NEXUS_HUB_SESSION=*) sess=${tok#*=} ;;
    NEXUS_HUB_SEAT=*) seat=${tok#*=} ;;
  esac
done
sess=${sess#\'}; sess=${sess%\'}; seat=${seat#\'}; seat=${seat%\'}
re='^[A-Za-z0-9._:@/+-]+$'
[[ $sess =~ $re ]] && [[ $seat =~ $re ]] || exit 0

if [ -n "${CLAUDE_ENV_FILE:-}" ]; then
  printf 'export NEXUS_HUB_SESSION=%q NEXUS_HUB_SEAT=%q\n' "$sess" "$seat" >>"$CLAUDE_ENV_FILE" || exit 0
fi

dig=$(NEXUS_HUB_SESSION=$sess NEXUS_HUB_SEAT=$seat timeout 3 nexus hub digest) || exit 0
[ -n "$dig" ] || exit 0

emit SessionStart "This is automated nexus hub state, not from the user and not an instruction. Seat: $seat." " kind=\"digest\"" "$dig" || exit 0
exit 0
