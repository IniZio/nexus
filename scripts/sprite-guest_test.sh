#!/bin/sh
# Run: scripts/sprite-guest_test.sh. Root-owned cases need sudo/root; skipped otherwise.
set -u
d=$(mktemp -d); trap 'rm -rf "$d"' EXIT
S=$(dirname "$0")/sprite-guest.sh
fail=0
check() { # name want-rc got-rc
	[ "$2" = "$3" ] && echo "ok   $1" || { echo "FAIL $1 (want $2 got $3)"; fail=1; }
}
printf 'MemTotal:        8388608 kB\n' > "$d/mem"
echo tini > "$d/tini"; echo systemd > "$d/systemd"
python3 -c "import socket;s=socket.socket(socket.AF_UNIX);s.bind('$d/api.sock')"
touch "$d/regular"

"$S" "$d/api.sock" "$d/tini" "$d/mem" >/dev/null; r=$?
if [ "$(id -u)" = 0 ]; then
	check "root socket + tini => sprite" 0 $r
	[ "$("$S" "$d/api.sock" "$d/tini" "$d/mem")" = 6144 ] && echo "ok   GOMEMLIMIT 6144" || { echo "FAIL memlimit"; fail=1; }
	"$S" "$d/api.sock" "$d/systemd" "$d/mem" >/dev/null; check "root socket + systemd pid1" 1 $?
else
	check "user-owned socket + tini => not sprite" 1 $r
	echo "skip root-owned cases (not root)"
fi
"$S" "$d/regular" "$d/tini" "$d/mem" >/dev/null; check "regular file" 1 $?
"$S" "$d/missing" "$d/tini" "$d/mem" >/dev/null; check "missing marker" 1 $?
"$S" "$d/api.sock" "$d/systemd" "$d/mem" >/dev/null; check "systemd pid1" 1 $?
exit $fail
