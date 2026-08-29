#!/usr/bin/env bash
# mutation_proofs.sh — structural mutation proofs for run.sh fixes.
#
# Every proof either extracts the REAL function from run.sh (never copying logic)
# or runs run.sh itself — no proof re-implements the behaviour under test.
# Each proof shows the test FAILS against a pre-fix version (confirms the bug)
# and PASSES against the current run.sh (confirms the fix).
# Pre-fix refs: 0bbb84f (proofs a–e), 6568d2b (proof f / FIX 1), 0c773bd (proof g / W17 over-report),
#               7f9208f (proof h / W18 freespace-failopen).
# FIX 6(c): proof (i) has NO genuine pre-fix ref — ^[0-9a-f]{64}$ is already
# present in 7f9208f and 2ce75ea. Proof (i) is MUTANT-ONLY: it demonstrates
# that deleting the digest-check block makes invalid sha256 output fall through
# to HASH_FAIL( (fabricated truncation). 7f9208f is NOT listed as its pre-fix ref.
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

_extract_verdict() {
    # Extract the real verdict if/elif/else/fi block from run.sh — identified by
    # the unique "if (( TOTAL_TRUNC > 0 ))" guard. Using the real block (not a
    # hand-written replica) means deleting this block from run.sh breaks the proof.
    local src="$1"
    awk '/^if \(\( TOTAL_TRUNC > 0 \)\); then/{p=1} p{print} p && /^fi$/{exit}' "$src"
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
        _extract_func "$src" "count_trunc_evidence"
        printf 'log() { echo "$*"; }\n'
        printf 'TOTAL_FAIL=1\n'
        printf 'TOTAL_TRUNC=0\n'
        # A genuine hash mismatch: got_hash is a valid 64-hex-char digest differing from expected.
        printf '%s\n' 'hash_line="StageB: file_32m=HASH_FAIL(exp=abc123456789...,got=def456012345...)"'
        printf '%s\n' '_th=$(count_trunc_evidence "$hash_line")'
        printf '%s\n' '(( TOTAL_TRUNC += _th )) || true'
        # Extract the REAL verdict block from run.sh. Deleting that block from run.sh
        # causes f/post to fail — this is the proof's mutation bite.
        _extract_verdict "$src"
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

# ── PROOF (g): dead hash probe (no /testfiles) → HARNESS INTEGRITY FAILURE ────
# Bug (W17 / FIX 1 over-report): debugfs exits 0 for a missing path and creates no
# output file. HEAD 0c773bd: sha256sum runs on the absent file → fails → got_hash set
# to "SHA256_FAILED" (a non-digest string) → HASH_FAIL(exp=…,got=SHA256_FAILE…)
# emitted → count_trunc_evidence counts HASH_FAIL( → TOTAL_TRUNC ≥ 1 →
# "TRUNCATION REPRODUCED". A dead probe fabricates a reproduction.
# Post-fix: !-s guard fires before sha256sum → emit_fail("dump_no_output,…") →
# HARNESS_INTEGRITY_FAIL(…) → count_trunc_evidence returns 0 → TOTAL_TRUNC=0,
# TOTAL_FAIL=1 → "HARNESS INTEGRITY FAILURE".
echo ""
echo "=== PROOF (g): dead hash probe (no /testfiles in ext4) → HARNESS INTEGRITY FAILURE, not TRUNCATION REPRODUCED ==="

# EXT4_IMG (created at top, no /testfiles) simulates a missing-path dead probe.
PRE_HEAD="$SCRATCHPAD/run_head.sh"
git show 0c773bd:internal/test/repro/run.sh > "$PRE_HEAD"

_run_dead_probe_verdict() {
    local src="$1"
    local gscript="$SCRATCHPAD/proof_g_$$.sh"
    local g_verify="$SCRATCHPAD/g_verify_$$"
    {
        _extract_func "$src" "emit_fail"
        _extract_func "$src" "stage_b_verify_hashes"
        _extract_func "$src" "count_trunc_evidence"
        printf 'log() { echo "$*"; }\n'
        printf 'VERIFY_TMPDIR="%s"\n' "$g_verify"
        printf 'mkdir -p "$VERIFY_TMPDIR"\n'
        printf 'HASH_VERIFIED_FILES=(file_32m file_elf)\n'
        # 64-hex-char expected hashes — arbitrary but format-valid
        printf 'declare -A EXPECTED_HASHES=([file_32m]="%s" [file_elf]="%s")\n' \
            "aabbccdd11223344aabbccdd11223344aabbccdd11223344aabbccdd11223344" \
            "11223344aabbccddaabbccdd1122334411223344aabbccdd11223344aabbccdd"
        printf 'TOTAL_FAIL=0\n'
        printf 'TOTAL_TRUNC=0\n'
        # stage_b_verify_hashes returns 1 when any_fail=1; capture both output and failure.
        printf 'hash_line=$(stage_b_verify_hashes "%s") || (( TOTAL_FAIL++ )) || true\n' "$EXT4_IMG"
        printf '_th=$(count_trunc_evidence "$hash_line")\n'
        printf '(( TOTAL_TRUNC += _th )) || true\n'
        # Real verdict block — extracted so deleting it from run.sh breaks this proof.
        _extract_verdict "$src"
    } > "$gscript"
    bash "$gscript" 2>/dev/null || true
    rm -f "$gscript"
    rm -rf "$g_verify"
}

verdict_g_pre=$(_run_dead_probe_verdict "$PRE_HEAD")
echo "  PRE-FIX  (HEAD 0c773bd) verdict: '$verdict_g_pre'"
assert_contains     "g/pre: HEAD dead probe → TRUNCATION REPRODUCED (confirms bug; proof valid)" \
    "$verdict_g_pre" "TRUNCATION REPRODUCED"
assert_not_contains "g/pre: HEAD does NOT emit HARNESS INTEGRITY FAILURE" \
    "$verdict_g_pre" "HARNESS INTEGRITY FAILURE"

verdict_g_post=$(_run_dead_probe_verdict "$POST_RUN")
echo "  POST-FIX verdict: '$verdict_g_post'"
assert_contains     "g/post: post-fix dead probe → HARNESS INTEGRITY FAILURE" \
    "$verdict_g_post" "HARNESS INTEGRITY FAILURE"
assert_not_contains "g/post: post-fix NOT TRUNCATION REPRODUCED" \
    "$verdict_g_post" "TRUNCATION REPRODUCED"

# ── PROOF (h): dead ext4_free_mb (stats yields nothing) → HARNESS_INTEGRITY_FAIL(FREESPACE_PROBE_DEAD ──
# Bug: pre-fix ext4_free_mb coerces empty stats to integer 0 via bash arithmetic.
# fill_ext4_pressure then computes fill_mb=(0-target)<0, silently returns 1, and
# the Phase 5 caller logs "fill skipped (already at/below target)" — zero builds
# run, zero failures emitted, exit 0. A dead instrument reports clean.
# Post-fix: ext4_free_mb returns DEBUGFS_ERR, fill_ext4_pressure emits
# HARNESS_INTEGRITY_FAIL(FREESPACE_PROBE_DEAD and increments TOTAL_FAIL, the
# Phase 5 caller logs "fill aborted due to error", and the verdict is
# HARNESS INTEGRITY FAILURE — never NO TRUNCATION OBSERVED.
# PROOF: run the full script with --disk-pressure and a debugfs stub that yields
# nothing for stats (simulating absent/unclean/held buildkit.ext4).
echo ""
echo "=== PROOF (h): dead ext4_free_mb probe → HARNESS_INTEGRITY_FAIL(FREESPACE_PROBE_DEAD + HARNESS INTEGRITY FAILURE ==="

PRE7F="$SCRATCHPAD/run_7f9208f.sh"
git show 7f9208f:internal/test/repro/run.sh > "$PRE7F"

# Override debugfs stub: returns nothing for any command (including stats).
STUB_DIR_H="$SCRATCHPAD/stubs_h"
cp -r "$STUB_DIR/." "$STUB_DIR_H/"
cat > "$STUB_DIR_H/debugfs" << 'STUB'
#!/usr/bin/env bash
exit 0
STUB
chmod +x "$STUB_DIR_H/debugfs"

run_h_script() {
    local src="$1"
    PATH="$STUB_DIR_H:$PATH" \
    NEXUS3="nexus3" \
    NEXUS3_STATE_DIR="$NEXUS3_STATE" \
    NEXUS3_BUILD_TASK_TIMEOUT="5s" \
    BASH_BUILD_TIMEOUT=10 \
    bash "$src" 1 --disk-pressure 2>&1 || true
}

pre_h=$(run_h_script "$PRE7F")
post_h=$(run_h_script "$POST_RUN")

pre_h_result=$(echo "$pre_h" | grep 'RESULT:' | tail -1)
post_h_result=$(echo "$post_h" | grep 'RESULT:' | tail -1)
echo "  PRE-FIX  (7f9208f) RESULT: $pre_h_result"
echo "  POST-FIX RESULT: $post_h_result"

# Pre-fix: dead probe silently coerces to 0 → fill skipped → no FREESPACE_PROBE_DEAD token.
assert_not_contains \
    "h/pre: pre-fix does NOT emit HARNESS_INTEGRITY_FAIL(FREESPACE_PROBE_DEAD (confirms bug; proof valid)" \
    "$pre_h" "HARNESS_INTEGRITY_FAIL(FREESPACE_PROBE_DEAD"

# Post-fix: dead probe produces FREESPACE_PROBE_DEAD token, verdict is HARNESS INTEGRITY FAILURE.
assert_contains \
    "h/post: post-fix emits HARNESS_INTEGRITY_FAIL(FREESPACE_PROBE_DEAD" \
    "$post_h" "HARNESS_INTEGRITY_FAIL(FREESPACE_PROBE_DEAD"
assert_contains \
    "h/post: verdict is HARNESS INTEGRITY FAILURE" \
    "$post_h_result" "HARNESS INTEGRITY FAILURE"
assert_not_contains \
    "h/post: verdict is NOT NO TRUNCATION OBSERVED" \
    "$post_h_result" "NO TRUNCATION OBSERVED"

# FIX 6(a): extend (h) to cover the restore_ext4_pressure call site.
# Deleting the DEBUGFS_ERR guard from restore_ext4_pressure left the suite green
# because the existing assertion matched FREESPACE_PROBE_DEAD from the fill path.
# The restore path emits "FREESPACE_PROBE_DEAD,restore" — assert it specifically.
assert_contains \
    "h/post: restore path emits FREESPACE_PROBE_DEAD,restore" \
    "$post_h" "FREESPACE_PROBE_DEAD,restore"

# Restore-mutant: delete the DEBUGFS_ERR guard from restore_ext4_pressure,
# confirm the restore-specific assertion fails (proof has bite on this call site).
# Uses targeted string-search (not fragile regex) to find and remove only the
# restore guard block identified by its unique FREESPACE_PROBE_DEAD,restore anchor.
MUTANT_H_RESTORE="$SCRATCHPAD/run_mutant_h_restore.sh"
cp "$POST_RUN" "$MUTANT_H_RESTORE"
python3 - "$MUTANT_H_RESTORE" << 'PYEOF'
import sys
src = open(sys.argv[1]).read()
# Locate the FREESPACE_PROBE_DEAD,restore anchor — unique to the restore block.
anchor = 'FREESPACE_PROBE_DEAD,restore'
idx = src.find(anchor)
if idx == -1:
    print("ERROR: anchor not found in mutant target", file=sys.stderr)
    sys.exit(1)
# Find the start of the enclosing if block (look backwards for the if line).
if_start = src.rfind('    if [[ "$free_mb" == "DEBUGFS_ERR" ]]', 0, idx)
if if_start == -1:
    print("ERROR: if-block start not found", file=sys.stderr)
    sys.exit(1)
# Find the else line and fi line after the anchor.
else_idx = src.find('    else\n', idx)
if else_idx == -1:
    print("ERROR: else branch not found", file=sys.stderr)
    sys.exit(1)
else_content_start = else_idx + len('    else\n')
fi_idx = src.find('    fi\n', else_content_start)
if fi_idx == -1:
    print("ERROR: fi not found", file=sys.stderr)
    sys.exit(1)
fi_end = fi_idx + len('    fi\n')
# Replace the whole if/else/fi with just the else-branch content (guard deleted).
else_content = src[else_content_start:fi_idx]
patched = src[:if_start] + else_content + src[fi_end:]
open(sys.argv[1], 'w').write(patched)
PYEOF
mutant_h_restore=$(run_h_script "$MUTANT_H_RESTORE")
echo "  RESTORE-MUTANT result (last 2 lines): $(echo "$mutant_h_restore" | tail -2)"
assert_not_contains \
    "h/restore-mutant: deleting restore guard removes FREESPACE_PROBE_DEAD,restore (proof has bite)" \
    "$mutant_h_restore" "FREESPACE_PROBE_DEAD,restore"

# FIX 6(b): explicit fill-call-site mutation proof — assert the failure mode
# rather than relying on a set -u abort. Prior behaviour: deleting the
# [[ "$free_mb" == "DEBUGFS_ERR" ]] guard from fill_ext4_pressure caused
# $(( DEBUGFS_ERR - target )) to expand "DEBUGFS_ERR" as a variable name →
# set -u abort → proof passed for the wrong reason (crash, not bug assertion).
#
# Correct mutant: change ext4_free_mb to return arithmetic 0 when probe is dead
# (the pre-fix behaviour) — the fill guard [[ "0" == "DEBUGFS_ERR" ]] never
# fires, fill_mb becomes negative, return 1 → builds skipped → NO TRUNCATION.
# This avoids the crash and produces a deterministic assertion.
MUTANT_H_FILL="$SCRATCHPAD/run_mutant_h_fill.sh"
cp "$POST_RUN" "$MUTANT_H_FILL"
python3 - "$MUTANT_H_FILL" << 'PYEOF'
import sys
src = open(sys.argv[1]).read()
# Change ext4_free_mb to return arithmetic result even when _out is empty
# (pre-fix behavior: 0 instead of "DEBUGFS_ERR"). The fill guard checking
# [[ "$free_mb" == "DEBUGFS_ERR" ]] never fires → dead instrument reports clean.
old = '    if [[ -z "$_out" ]]; then echo "DEBUGFS_ERR"; else echo $(( _out * 4096 / 1024 / 1024 )); fi'
new = '    echo $(( _out * 4096 / 1024 / 1024 ))'
if old not in src:
    print("ERROR: ext4_free_mb guard not found in mutant target", file=sys.stderr)
    sys.exit(1)
open(sys.argv[1], 'w').write(src.replace(old, new, 1))
PYEOF
mutant_h_fill=$(run_h_script "$MUTANT_H_FILL")
mutant_h_fill_result=$(echo "$mutant_h_fill" | grep 'RESULT:' | tail -1)
echo "  FILL-MUTANT RESULT: $mutant_h_fill_result"
# Key assertion: without the fill guard, a dead ext4_free_mb probe is SILENT —
# the operator never learns the disk condition was unknown during Phase 5.
# Note: FIX 3 (fill_rc==1 runs builds) means stubs produce NO_NEW_IMAGE →
# HARNESS INTEGRITY FAILURE via a different path, NOT "NO TRUNCATION OBSERVED".
# The critical property this assertion covers: FREESPACE_PROBE_DEAD,fill is absent
# (the harness cannot distinguish a real "already pressured" from a dead probe).
assert_not_contains \
    "h/fill-mutant: dead probe returns 0 → FREESPACE_PROBE_DEAD,fill absent (probe dead is silent)" \
    "$mutant_h_fill" "FREESPACE_PROBE_DEAD,fill"
# Confirm the post-fix (with guard intact) DOES emit FREESPACE_PROBE_DEAD,fill
# with the same dead debugfs stub — this gives the mutant assertion its bite.
assert_contains \
    "h/fill-mutant (post-fix control): guard intact → FREESPACE_PROBE_DEAD,fill emitted" \
    "$post_h" "FREESPACE_PROBE_DEAD,fill"

# ── PROOF (i): digest-check unique case — non-empty dump with invalid sha256 output ──
# Covers the ^[0-9a-f]{64}$ guard's unique case: a file that passes [[ ! -s ]] (non-empty
# dump output exists) but whose sha256sum output is NOT a valid 64-hex digest.
# Without this guard the function would try to compare a garbage got_hash against
# exp_hash, and the [[ != ]] branch would emit HASH_FAIL( (counting as truncation
# evidence), fabricating a reproduction. The guard routes it through emit_fail instead.
# PROOF: stub sha256sum to produce "ABC" (non-hex, non-64-char) and debugfs to write
# a 1-byte file to simulate a non-empty dump. Confirm post-fix emits
# HARNESS_INTEGRITY_FAIL(sha256_failed and NOT HASH_FAIL(. Mutation: delete the
# digest-check block from a scratch copy and confirm the proof fails.
echo ""
echo "=== PROOF (i): non-empty dump with invalid sha256 output → HARNESS_INTEGRITY_FAIL(sha256_failed, not HASH_FAIL( ==="

STUB_DIR_I="$SCRATCHPAD/stubs_i"
mkdir -p "$STUB_DIR_I"
# sha256sum stub: always outputs "ABC  filename" (non-hex, 3 chars — fails ^[0-9a-f]{64}$).
cat > "$STUB_DIR_I/sha256sum" << 'STUB'
#!/usr/bin/env bash
echo "ABC  $1"
STUB
chmod +x "$STUB_DIR_I/sha256sum"
# debugfs stub: for dump writes a 1-byte file to the output path (non-empty → passes [[ ! -s ]]).
STUB_DIR_I_DBFS="$STUB_DIR_I/debugfs"
cat > "$STUB_DIR_I_DBFS" << 'STUBEOF'
#!/usr/bin/env bash
# Parse: debugfs -R "dump /testfiles/FILE OUTPATH" IMG
# Extract OUTPATH from the -R argument.
for arg in "$@"; do
    if [[ "$arg" =~ ^dump[[:space:]] ]]; then
        out_path=$(echo "$arg" | awk '{print $3}')
        echo "x" > "$out_path"
        break
    fi
done
exit 0
STUBEOF
chmod +x "$STUB_DIR_I_DBFS"

_run_digest_check() {
    local src="$1"
    local iscript="$SCRATCHPAD/proof_i_$$.sh"
    local i_verify="$SCRATCHPAD/i_verify_$$"
    {
        _extract_func "$src" "emit_fail"
        _extract_func "$src" "stage_b_verify_hashes"
        _extract_func "$src" "count_trunc_evidence"
        printf 'log() { echo "$*"; }\n'
        printf 'VERIFY_TMPDIR="%s"\n' "$i_verify"
        printf 'mkdir -p "$VERIFY_TMPDIR"\n'
        printf 'HASH_VERIFIED_FILES=(file_elf)\n'
        printf 'declare -A EXPECTED_HASHES=([file_elf]="%s")\n' \
            "aabbccdd11223344aabbccdd11223344aabbccdd11223344aabbccdd11223344"
        printf 'TOTAL_FAIL=0\n'
        printf 'TOTAL_TRUNC=0\n'
        printf 'hash_line=$(PATH="%s:$PATH" stage_b_verify_hashes "dummy.img") || (( TOTAL_FAIL++ )) || true\n' "$STUB_DIR_I"
        printf '_th=$(count_trunc_evidence "$hash_line")\n'
        printf '(( TOTAL_TRUNC += _th )) || true\n'
        printf 'echo "hash_line=${hash_line}"\n'
        _extract_verdict "$src"
    } > "$iscript"
    PATH="$STUB_DIR_I:$PATH" bash "$iscript" 2>/dev/null || true
    rm -f "$iscript"
    rm -rf "$i_verify"
}

# Scratch copy for mutation: delete the digest-check block.
MUTANT_I="$SCRATCHPAD/run_mutant_i.sh"
cp "$POST_RUN" "$MUTANT_I"
# Delete the 5-line digest-check block (from the regex guard through the continue).
python3 - "$MUTANT_I" << 'PYEOF'
import sys, re
src = open(sys.argv[1]).read()
# Remove the digest-check block: if [[ ! "$got_hash" =~ ... ]]; then ... continue\n    fi
patched = re.sub(
    r"        # A real sha256 digest.*?        fi\n",
    "",
    src,
    flags=re.DOTALL
)
open(sys.argv[1], 'w').write(patched)
PYEOF

post_i=$(_run_digest_check "$POST_RUN")
mutant_i=$(_run_digest_check "$MUTANT_I")

echo "  POST-FIX result (last 3 lines): $(echo "$post_i" | tail -3)"
echo "  MUTANT   result (last 3 lines): $(echo "$mutant_i" | tail -3)"

assert_contains \
    "i/post: post-fix emits HARNESS_INTEGRITY_FAIL(sha256_failed" \
    "$post_i" "HARNESS_INTEGRITY_FAIL(sha256_failed"
assert_not_contains \
    "i/post: post-fix does NOT emit HASH_FAIL(" \
    "$post_i" "HASH_FAIL("
# Mutation: without the digest-check block, invalid sha256 output falls through to
# the [[ != ]] comparison and emits HASH_FAIL( instead of sha256_failed.
assert_contains \
    "i/mutant: without digest check, emits HASH_FAIL( (confirms proof has bite)" \
    "$mutant_i" "HASH_FAIL("
assert_not_contains \
    "i/mutant: without digest check, does NOT emit sha256_failed" \
    "$mutant_i" "HARNESS_INTEGRITY_FAIL(sha256_failed"

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
