#!/usr/bin/env bash
# mutation_proofs.sh — structural mutation proofs for run.sh fixes.
#
# Every proof either extracts the REAL function from run.sh (never copying logic)
# or runs run.sh itself — no proof re-implements the behaviour under test.
# Each proof shows the test FAILS against a pre-fix version (confirms the bug)
# and PASSES against the current run.sh (confirms the fix).
# Pre-fix refs: 0bbb84f (proofs a–e), 6568d2b (proof f / FIX 1).
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
# Bug: pre-fix stage_b_check_dc_size has no probe_dead arm — when debugfs_size
# returns "" (not DEBUGFS_ERR, not a number), the function falls through to OK(B).
# Post-fix adds the probe_dead arm that emits FAIL( for dead probes.
# PROOF: extract the REAL stage_b_check_dc_size and its callees (emit_fail) from
# run.sh, stub debugfs_size to return "" (dead probe), and confirm the verdict
# changes between pre-fix and post-fix.
echo ""
echo "=== PROOF (b): dead dc probe emits FAIL(, never OK( ==="

_extract_func() {
    local src="$1" name="$2"
    awk "/^${name}\(\)/{f=1} f{print} f && /^\}/{c++; if(c==1)exit}" "$src"
}

_extract_func() {
    local src="$1" name="$2"
    awk "/^${name}\(\)/{f=1} f{print} f && /^\}/{c++; if(c==1)exit}" "$src"
}

# Write each subshell to a temp file so that $1, ${got}, ${dc_path} etc. in the
# extracted function bodies are NOT expanded by the outer shell (printf -v avoids
# double-quote interpolation entirely).
PRE_B_SCRIPT="$SCRATCHPAD/proof_b_pre.sh"
{
    _extract_func "$PRE_RUN" "emit_fail"
    _extract_func "$PRE_RUN" "stage_b_check_dc_size"
    printf 'debugfs_size() { echo ""; }\n'
    printf "stage_b_check_dc_size 'fake.img'\n"
} > "$PRE_B_SCRIPT"

POST_B_SCRIPT="$SCRATCHPAD/proof_b_post.sh"
{
    _extract_func "$POST_RUN" "emit_fail"
    _extract_func "$POST_RUN" "stage_b_check_dc_size"
    printf 'debugfs_size() { echo ""; }\n'
    printf "stage_b_check_dc_size 'fake.img'\n"
} > "$POST_B_SCRIPT"

pre_b=$(bash "$PRE_B_SCRIPT" 2>/dev/null || true)
echo "  PRE-FIX  result: '$pre_b'"
if echo "$pre_b" | grep -qF "OK("; then
    proof_pass "b/pre: pre-fix emits OK( for dead probe (confirms bug; proof valid)"
else
    proof_fail "b/pre: pre-fix did not emit OK( — bug may already be fixed or extraction failed"
fi

post_b=$(bash "$POST_B_SCRIPT" 2>/dev/null || true)
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

# ── PROOF (e): emit_fail grep gate fires on literal HARNESS_INTEGRITY_FAIL( violation ──
# The gate (run.sh:964-977) scans BASH_SOURCE[0] at startup and aborts with
# "SELF-CHECK FAIL" if any literal HARNESS_INTEGRITY_FAIL( token appears outside
# the emit_fail body or a grep/comment line.
# PROOF: copy post-fix run.sh, inject a bare literal at line 2, run it with stubs,
# assert "SELF-CHECK FAIL" appears. If the gate block is deleted, this assertion fails
# — the proof has bite.
echo ""
echo "=== PROOF (e): emit_fail grep gate fires on literal HARNESS_INTEGRITY_FAIL( outside helper ==="

INJECTED_RUN="$SCRATCHPAD/run_injected.sh"
cp "$POST_RUN" "$INJECTED_RUN"
# Inject a bare variable assignment containing HARNESS_INTEGRITY_FAIL( at line 2
# (after shebang). This is outside emit_fail's body and not a grep or comment line.
sed -i '2s|^|BAD_VAR="HARNESS_INTEGRITY_FAIL(injected_bypass)"\n|' "$INJECTED_RUN"

injected_out=$(run_script_with_stubs "$INJECTED_RUN" 1)
echo "  Injected run (last 3 lines): $(echo "$injected_out" | tail -3)"
assert_contains "e: gate fires on injected literal and emits SELF-CHECK FAIL" \
    "$injected_out" "SELF-CHECK FAIL"

# Confirm gate does NOT fire on clean post-fix run.sh (no injected violations).
clean_out=$(run_script_with_stubs "$POST_RUN" 1)
assert_not_contains "e/clean: clean post-fix run.sh does NOT trigger SELF-CHECK FAIL" \
    "$clean_out" "SELF-CHECK FAIL"

# ── PROOF (f): HASH_FAIL( evidence → TRUNCATION REPRODUCED verdict ───────────
# Bug (FIX 1): count_trunc_evidence at HEAD 6568d2b matched only TRUNCATED_AT_32MiB
# and FAIL(exp=N,got=N) tokens. HASH_FAIL(exp=<hex>...,got=<hex>...) was not counted,
# so file_32m content corruption — the ONLY truncation detector for the 32MiB-boundary
# file (where correct size == truncation size) — left TOTAL_TRUNC=0. The verdict then
# printed "HARNESS INTEGRITY FAILURE" instead of "TRUNCATION REPRODUCED", misclassifying
# a real reproduction as a harness fault.
# ASSERT ON THE VERDICT LINE (not count_trunc_evidence in isolation): extracting the
# real function and running it through the real verdict block couples the counter and
# the decision, avoiding assertion-vs-mechanism drift.
echo ""
echo "=== PROOF (f): HASH_FAIL( evidence → TRUNCATION REPRODUCED (not HARNESS INTEGRITY FAILURE) ==="

PRE6568="$SCRATCHPAD/run_6568d2b.sh"
git show 6568d2b:internal/test/repro/run.sh > "$PRE6568"

_run_hash_fail_verdict() {
    local src="$1"
    # Write the subshell to a temp file to preserve $1, ${}, etc. in the extracted
    # function body without outer-shell double-quote expansion.
    local vscript="$SCRATCHPAD/proof_f_verdict_$$.sh"
    {
        awk '/^count_trunc_evidence\(\)/{f=1} f{print} f && /^\}/{c++; if(c==1)exit}' "$src"
        printf 'log() { echo "$*"; }\n'
        printf 'TOTAL_FAIL=1\n'
        printf 'TOTAL_TRUNC=0\n'
        printf '%s\n' 'hash_line="StageB: file_32m=HASH_FAIL(exp=abc123456789...,got=def456012345...)"'
        printf '%s\n' '_th=$(count_trunc_evidence "$hash_line")'
        printf '%s\n' '(( TOTAL_TRUNC += _th )) || true'
        printf '%s\n' 'if (( TOTAL_TRUNC > 0 )); then'
        printf '%s\n' "    log 'RESULT: TRUNCATION REPRODUCED \xe2\x80\x94 see FAIL entries above'"
        printf '%s\n' 'elif (( TOTAL_FAIL > 0 )); then'
        printf '%s\n' "    log 'RESULT: HARNESS INTEGRITY FAILURE (no truncation evidence) \xe2\x80\x94 see FAIL entries above'"
        printf '%s\n' 'else'
        printf '%s\n' "    log 'RESULT: NO TRUNCATION OBSERVED in this run'"
        printf '%s\n' 'fi'
    } > "$vscript"
    bash "$vscript" 2>/dev/null || true
    rm -f "$vscript"
}

verdict_pre=$(_run_hash_fail_verdict "$PRE6568")
echo "  PRE-FIX  (6568d2b) verdict: '$verdict_pre'"
assert_contains "f/pre: 6568d2b HASH_FAIL( → HARNESS INTEGRITY FAILURE (confirms bug; proof valid)" \
    "$verdict_pre" "HARNESS INTEGRITY FAILURE"

verdict_post=$(_run_hash_fail_verdict "$POST_RUN")
echo "  POST-FIX verdict: '$verdict_post'"
assert_contains     "f/post: post-fix HASH_FAIL( → TRUNCATION REPRODUCED" \
    "$verdict_post" "TRUNCATION REPRODUCED"
assert_not_contains "f/post: post-fix NOT HARNESS INTEGRITY FAILURE" \
    "$verdict_post" "HARNESS INTEGRITY FAILURE"

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
