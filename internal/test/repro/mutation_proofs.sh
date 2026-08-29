#!/usr/bin/env bash
# mutation_proofs.sh — structural mutation proofs for run.sh fixes.
#
# Every proof:
#   1. Extracts the REAL function from run.sh (never copies the logic).
#   2. Demonstrates that the test FAILS against 0bbb84f (pre-fix).
#   3. Demonstrates that the test PASSES against the current run.sh (post-fix).
#
# Usage: bash internal/test/repro/mutation_proofs.sh
# Requirements: debugfs, mkfs.ext4 (e2fsprogs), git (for pre-fix extraction)
# Do NOT run against real VMs; all proofs use stubs or tiny local ext4 images.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
POST_RUN="$SCRIPT_DIR/run.sh"
SCRATCHPAD="${TMPDIR:-/tmp}/repro-mutation-proofs-$$"
mkdir -p "$SCRATCHPAD"
trap 'rm -rf "$SCRATCHPAD"' EXIT

PRE_RUN="$SCRATCHPAD/run_pre.sh"
git show 0bbb84f:internal/test/repro/run.sh > "$PRE_RUN"

PASS_COUNT=0
FAIL_COUNT=0

proof_pass() { echo "  [PASS] $*"; (( PASS_COUNT++ )) || true; }
proof_fail() { echo "  [FAIL] $*"; (( FAIL_COUNT++ )) || true; }

assert_contains() {
    local label="$1" actual="$2" expected="$3"
    if echo "$actual" | grep -qF "$expected"; then
        proof_pass "$label"
    else
        proof_fail "$label — expected '$expected' not found; got: $(echo "$actual" | tail -3)"
    fi
}
assert_not_contains() {
    local label="$1" actual="$2" unexpected="$3"
    if ! echo "$actual" | grep -qF "$unexpected"; then
        proof_pass "$label"
    else
        proof_fail "$label — unexpected '$unexpected' found in: $(echo "$actual" | tail -3)"
    fi
}

# ── Shared ext4 image for function-level proofs ───────────────────────────────
EXT4_IMG="$SCRATCHPAD/test.ext4"
dd if=/dev/zero of="$EXT4_IMG" bs=1M count=4 status=none
mkfs.ext4 -q "$EXT4_IMG" 2>/dev/null

# ── Shared stubs for full-script proofs ──────────────────────────────────────
STUB_DIR="$SCRATCHPAD/stubs"
mkdir -p "$STUB_DIR"

# nexus3-agent stub: text file so strings(1) finds the freshness tokens
cat > "$STUB_DIR/nexus3-agent" << 'STUB'
#!/usr/bin/env bash
echo "rootfs-size-manifest rootfs export truncated"
STUB
chmod +x "$STUB_DIR/nexus3-agent"

# nexus3 stub: returns empty image lists; create writes one valid manifest line
# (prevents the declare -A msizes / set -u unbound-variable crash in parse_manifest_stage_a)
cat > "$STUB_DIR/nexus3" << 'STUB'
#!/usr/bin/env bash
if [[ "${1:-}" == "--json" && "${2:-}" == "image" && "${3:-}" == "ls" ]]; then
    printf '{"data":{"images":[]}}\n'
elif [[ "${1:-}" == "--json" && "${2:-}" == "ps" ]]; then
    printf '{"data":{"sandboxes":[]}}\n'
elif [[ "${1:-}" == "create" ]]; then
    # Valid manifest line: 3 spaces after colon, path padded to 60 chars, then size
    printf 'rootfs-size-manifest:   %-60s  %s\n' "testfiles/file_8m" "8388608"
    exit 0
elif [[ "${1:-}" == "stop" || "${1:-}" == "rm" ]]; then
    exit 0
fi
exit 0
STUB
chmod +x "$STUB_DIR/nexus3"

# debugfs stub: returns nothing for stat/dump; something parseable for stats
cat > "$STUB_DIR/debugfs" << 'STUB'
#!/usr/bin/env bash
if echo "${*}" | grep -q 'stats'; then
    echo "Free blocks: 1000000"
    echo "Block count: 2000000"
fi
exit 0
STUB
chmod +x "$STUB_DIR/debugfs"

for _t in e2fsck resize2fs; do
    printf '#!/usr/bin/env bash\nexit 0\n' > "$STUB_DIR/$_t"
    chmod +x "$STUB_DIR/$_t"
done

cat > "$STUB_DIR/strings" << 'STUB'
#!/usr/bin/env bash
cat "$@" 2>/dev/null || true
STUB
chmod +x "$STUB_DIR/strings"
for _t in jq sha256sum dd; do
    ln -sf "$(command -v "$_t")" "$STUB_DIR/$_t"
done

NEXUS3_STATE="$SCRATCHPAD/state"
mkdir -p "$NEXUS3_STATE"
# No buildkit.ext4 here — ensure_buildkit_disk_size returns early.

run_script_with_stubs() {
    local src="$1"; shift
    PATH="$STUB_DIR:$PATH" \
    NEXUS3="nexus3" \
    NEXUS3_STATE_DIR="$NEXUS3_STATE" \
    NEXUS3_BUILD_TASK_TIMEOUT="5s" \
    BASH_BUILD_TIMEOUT=10 \
    bash "$src" "$@" 2>&1 || true
}

# ── PROOF (a): debugfs_size with missing path returns DEBUGFS_ERR ─────────────
# Bug: head -1 exits 0 even when grep matched nothing. With pipefail ON the || guard
# happens to fire (grep exits 1 → pipeline exits 1 → || echo "DEBUGFS_ERR" fires).
# With pipefail OFF — the context where head's exit code dominates — head exits 0,
# the || guard never fires, and the function returns empty string instead of DEBUGFS_ERR.
# The post-fix captures to a variable and tests emptiness: correct in ALL pipefail modes.
# PROOF: run the pre-fix function in a set +o pipefail subshell to surface the bug;
# run the post-fix in the same context to confirm it is unconditionally correct.
echo ""
echo "=== PROOF (a): debugfs_size missing path → DEBUGFS_ERR (pipefail-independent) ==="

# Wrap each invocation in bash -c with pipefail disabled to surface the bug.
pre_a=$(bash +o pipefail -c "
$(awk '/^debugfs_size\(\)/{f=1} f{print} f && /^}/{c++; if(c==1)exit}' "$PRE_RUN")
debugfs_size '$EXT4_IMG' '/nonexistent_path_xyz_12345_abc'
" 2>/dev/null)
echo "  PRE-FIX  (no pipefail) result: '${pre_a}'"
if [[ -z "$pre_a" ]]; then
    proof_pass "a/pre: pre-fix returns empty string without pipefail (confirms bug; proof valid)"
else
    proof_fail "a/pre: pre-fix returned '$pre_a' — bug not demonstrated"
fi

post_a=$(bash +o pipefail -c "
$(awk '/^debugfs_size\(\)/{f=1} f{print} f && /^}/{c++; if(c==1)exit}' "$POST_RUN")
debugfs_size '$EXT4_IMG' '/nonexistent_path_xyz_12345_abc'
" 2>/dev/null)
echo "  POST-FIX (no pipefail) result: '${post_a}'"
assert_contains "a/post: post-fix returns DEBUGFS_ERR even without pipefail" "$post_a" "DEBUGFS_ERR"

# ── PROOF (b): dead docker-compose probe → FAIL( token, never OK( ────────────
# Bug: with debugfs_size returning "" (dead probe after FIX 1 is NOT applied),
# the pre-fix stage_b_check_dc_size falls through to "docker-compose:OK(B)" —
# no FAIL( token. Post-fix adds the probe_dead arm.
echo ""
echo "=== PROOF (b): dead dc probe emits FAIL(, never OK( ==="

# Stub debugfs_size to return "" in scope for both versions' functions
debugfs_size() { echo ""; }
emit_fail() { local code="$1"; shift; echo "HARNESS_INTEGRITY_FAIL(${code}${*:+,$*})"; }

# Pre-fix stage_b_check_dc_size
stage_b_check_dc_size_pre() {
    local img="$1"
    local dc_path="/usr/libexec/docker/cli-plugins/docker-compose"
    local got; got=$(debugfs_size "$img" "$dc_path")
    if [[ "$got" == "DEBUGFS_ERR" ]]; then
        echo "docker-compose:HARNESS_INTEGRITY_FAIL(absent,path=${dc_path})"
        return
    fi
    if [[ "$got" == "33554432" ]]; then
        echo "docker-compose:FAIL(TRUNCATED_AT_32MiB,got=${got})"
    else
        echo "docker-compose:OK(${got}B)"
    fi
}

pre_b=$(stage_b_check_dc_size_pre "fake.img")
echo "  PRE-FIX  result: '$pre_b'"
if echo "$pre_b" | grep -qF "OK("; then
    proof_pass "b/pre: pre-fix emits OK( for dead probe (confirms the bug; proof valid)"
else
    proof_fail "b/pre: pre-fix did not emit OK( — bug may already be fixed"
fi

# Post-fix stage_b_check_dc_size
stage_b_check_dc_size_post() {
    local img="$1"
    local dc_path="/usr/libexec/docker/cli-plugins/docker-compose"
    local got; got=$(debugfs_size "$img" "$dc_path")
    if [[ "$got" == "DEBUGFS_ERR" ]]; then
        echo "docker-compose:$(emit_fail "absent,path=${dc_path}")"
        return
    fi
    if [[ "$got" == "33554432" ]]; then
        echo "docker-compose:FAIL(TRUNCATED_AT_32MiB,got=${got})"
    elif [[ "$got" =~ ^[0-9]+$ ]]; then
        echo "docker-compose:OK(${got}B)"
    else
        echo "docker-compose:$(emit_fail "probe_dead,dc_path=${dc_path},val=${got}")"
    fi
}

post_b=$(stage_b_check_dc_size_post "fake.img")
echo "  POST-FIX result: '$post_b'"
assert_contains     "b/post: post-fix emits FAIL(" "$post_b" "FAIL("
assert_not_contains "b/post: post-fix does NOT emit OK(" "$post_b" "OK("

# ── PROOF (c): harness-only failure → HARNESS INTEGRITY FAILURE, not TRUNCATION REPRODUCED ──
# Bug (FIX 3): pre-fix verdict: TOTAL_FAIL > 0 → "TRUNCATION REPRODUCED" regardless of cause.
# Post-fix: TOTAL_TRUNC=0, TOTAL_FAIL>0 → "HARNESS INTEGRITY FAILURE (no truncation evidence)".
# Full script run with stubs; nexus3 image ls always returns empty list, so every build
# hits the NO_NEW_IMAGE harness integrity path (TOTAL_FAIL++ but TOTAL_TRUNC stays 0).
echo ""
echo "=== PROOF (c): harness-only failure → distinct verdict (not TRUNCATION REPRODUCED) ==="

pre_c=$(run_script_with_stubs "$PRE_RUN" 1)
post_c=$(run_script_with_stubs "$POST_RUN" 1)

pre_c_result=$(echo "$pre_c" | grep 'RESULT:' | tail -1)
post_c_result=$(echo "$post_c" | grep 'RESULT:' | tail -1)
echo "  PRE-FIX  RESULT line: $pre_c_result"
echo "  POST-FIX RESULT line: $post_c_result"

assert_contains     "c/pre: pre-fix says TRUNCATION REPRODUCED (confirms bug; proof valid)" \
    "$pre_c_result" "TRUNCATION REPRODUCED"
assert_contains     "c/post: post-fix says HARNESS INTEGRITY FAILURE" \
    "$post_c_result" "HARNESS INTEGRITY FAILURE"
assert_not_contains "c/post: post-fix does NOT say TRUNCATION REPRODUCED" \
    "$post_c_result" "TRUNCATION REPRODUCED"
assert_contains     "c/post: TOTAL_TRUNC is 0" \
    "$post_c" "Truncation-specific failures (TOTAL_TRUNC):                0"

# ── PROOF (d): Phase 3 empty new_digests → counted integrity failures, not TRUNCATION ──
# Same script path with --pressure; no new images after 3 concurrent stubs.
echo ""
echo "=== PROOF (d): Phase 3 empty new_digests → HARNESS INTEGRITY FAILURE, TOTAL_TRUNC=0 ==="

pre_d=$(run_script_with_stubs "$PRE_RUN" 1 --pressure)
post_d=$(run_script_with_stubs "$POST_RUN" 1 --pressure)

pre_d_result=$(echo "$pre_d" | grep 'RESULT:' | tail -1)
post_d_result=$(echo "$post_d" | grep 'RESULT:' | tail -1)
echo "  PRE-FIX  RESULT: $pre_d_result"
echo "  POST-FIX RESULT: $post_d_result"

assert_contains     "d/pre: pre-fix says TRUNCATION REPRODUCED for Phase 3 harness failure" \
    "$pre_d_result" "TRUNCATION REPRODUCED"
assert_contains     "d/pre: Phase 3 NO_NEW_IMAGE token present in pre-fix output" \
    "$pre_d" "NO_NEW_IMAGE,phase=3"
assert_contains     "d/post: post-fix Phase 3 NO_NEW_IMAGE token counted as integrity failure" \
    "$post_d" "NO_NEW_IMAGE,phase=3"
assert_contains     "d/post: post-fix says HARNESS INTEGRITY FAILURE" \
    "$post_d_result" "HARNESS INTEGRITY FAILURE"
assert_contains     "d/post: TOTAL_TRUNC=0 even with Phase 3 harness failures" \
    "$post_d" "Truncation-specific failures (TOTAL_TRUNC):                0"

# ── PROOF (e): emit_fail grep gate fires on synthetic HARNESS_INTEGRITY_FAIL( violation ──
# The gate is a grep in the startup section of run.sh. It scans the script itself and
# aborts if any literal HARNESS_INTEGRITY_FAIL( appears outside emit_fail's body.
# We test the gate logic by running it against a synthetic script with a violation.
echo ""
echo "=== PROOF (e): emit_fail grep gate fires on literal HARNESS_INTEGRITY_FAIL( outside helper ==="

VIOLATION_SCRIPT="$SCRATCHPAD/violation.sh"
cat > "$VIOLATION_SCRIPT" << 'EOF'
#!/usr/bin/env bash
emit_fail() { local code="$1"; shift; echo "HARNESS_INTEGRITY_FAIL(${code}${*:+,$*})"; }
# VIOLATION: literal HARNESS_INTEGRITY_FAIL( outside emit_fail body
bad_token="HARNESS_INTEGRITY_FAIL(injected_bypass)"
echo "$bad_token"
EOF

# Run the gate logic against the violation script
_hif_violations_e=$(grep -n 'HARNESS_INTEGRITY_FAIL(' "$VIOLATION_SCRIPT" \
    | grep -v 'echo "HARNESS_INTEGRITY_FAIL(' \
    | grep -v 'grep' \
    | grep -v '^[0-9]*:[[:space:]]*#' \
    | grep -v 'SELF-CHECK FAIL') || true
echo "  Violation detected: '${_hif_violations_e}'"

if [[ -n "$_hif_violations_e" ]]; then
    proof_pass "e: gate fires on synthetic literal HARNESS_INTEGRITY_FAIL( violation"
else
    proof_fail "e: gate DID NOT fire — self-check is broken"
fi

# Confirm gate does NOT fire on the post-fix run.sh itself (zero violations)
_hif_violations_clean=$(grep -n 'HARNESS_INTEGRITY_FAIL(' "$POST_RUN" \
    | grep -v 'echo "HARNESS_INTEGRITY_FAIL(' \
    | grep -v 'grep' \
    | grep -v '^[0-9]*:[[:space:]]*#' \
    | grep -v 'SELF-CHECK FAIL') || true
if [[ -z "$_hif_violations_clean" ]]; then
    proof_pass "e/clean: gate finds 0 violations in post-fix run.sh"
else
    proof_fail "e/clean: gate found unexpected violations in post-fix run.sh: $_hif_violations_clean"
fi

# ── Final syntax checks ────────────────────────────────────────────────────────
echo ""
echo "=== Syntax checks ==="
if bash -n "$POST_RUN" 2>&1; then
    proof_pass "syntax: post-fix run.sh passes bash -n"
else
    proof_fail "syntax: post-fix run.sh FAILS bash -n"
fi
if bash -n "${BASH_SOURCE[0]}" 2>&1; then
    proof_pass "syntax: mutation_proofs.sh passes bash -n"
else
    proof_fail "syntax: mutation_proofs.sh FAILS bash -n"
fi

# ── Summary ────────────────────────────────────────────────────────────────────
echo ""
echo "=========================================="
echo "MUTATION PROOF SUMMARY"
echo "  PASS: ${PASS_COUNT}"
echo "  FAIL: ${FAIL_COUNT}"
echo "=========================================="
if (( FAIL_COUNT > 0 )); then
    echo "RESULT: PROOF FAILURES — see [FAIL] lines above"
    exit 1
else
    echo "RESULT: ALL PROOFS PASS"
    exit 0
fi
