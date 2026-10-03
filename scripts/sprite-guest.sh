#!/bin/sh
marker=${1:-/.sprite/api.sock}
comm=${2:-/proc/1/comm}
meminfo=${3:-/proc/meminfo}
[ -S "$marker" ] && [ ! -L "$marker" ] || exit 1
[ "$(stat -c %u "$marker" 2>/dev/null)" = 0 ] || exit 1
[ "$(cat "$comm" 2>/dev/null)" != systemd ] || exit 1
kb=$(awk '/^MemTotal:/ {print $2}' "$meminfo")
[ -n "$kb" ] || exit 1
echo $((kb * 3 / 4 / 1024))
