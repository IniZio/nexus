#!/usr/bin/env bash
set -euo pipefail

pass=0
fail=0

check() {
  local name="$1" got="$2" want="$3"
  if [ "$got" = "$want" ]; then
    echo "PASS: $name"
    (( ++pass ))
  else
    echo "FAIL: $name — got '$got', want '$want'"
    (( ++fail ))
  fi
}

detect_new_tag() {
  local before="$1" after="$2"
  comm -13 <(echo "$before" | sort) <(echo "$after" | sort) | grep -E '^v' | tail -1
}

BEFORE="v0.12.0-nightly.1
v0.12.0-nightly.20
v0.11.0"
AFTER="$BEFORE
v0.12.0"
check "stable_after_nightlies" "$(detect_new_tag "$BEFORE" "$AFTER")" "v0.12.0"

BEFORE2="v0.12.0-nightly.20"
AFTER2="v0.12.0-nightly.20
v0.12.0-nightly.21"
check "nightly_published" "$(detect_new_tag "$BEFORE2" "$AFTER2")" "v0.12.0-nightly.21"

check "no_release" "$(detect_new_tag "$BEFORE2" "$BEFORE2")" ""

check "first_release" "$(detect_new_tag "" "v0.1.0")" "v0.1.0"

echo ""
echo "Results: $pass passed, $fail failed"
[ "$fail" -eq 0 ] || exit 1
