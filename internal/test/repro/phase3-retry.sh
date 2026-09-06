#!/usr/bin/env bash
# phase3-retry.sh — targeted retry of Phase 3 (3 concurrent builds) only.
# Run AFTER at least one successful sequential/heavy build has populated
# buildkit.ext4 (avoids the pump:EOF-on-fresh-db failure pattern).
# Sources helper functions from run.sh via its env — executed standalone.
set -euo pipefail

NEXUS3="${NEXUS3:-nexus3}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE="$SCRIPT_DIR/workspace"
IMAGE_STORE="${NEXUS3_STATE_DIR:-$HOME/.local/state/nexus3}/images/sha256"
BUILD_LOG_DIR="$SCRIPT_DIR/logs"
VERIFY_TMPDIR="${TMPDIR:-/tmp}/repro-verify-p3retry-$$"
BASH_BUILD_TIMEOUT=1800
REPRO_PROJECT="repro"

log() { echo "[$(date +'%H:%M:%S')] $*"; }

list_digests_sorted() {
    "$NEXUS3" --json image ls 2>/dev/null \
        | jq -r '.data.images[].digest' 2>/dev/null \
        | sort || true
}

debugfs_size() {
    local img="$1" path="$2"
    debugfs -R "stat ${path}" "$img" 2>/dev/null \
        | grep -oP '(?<=Size: )\d+' | head -1 || echo "DEBUGFS_ERR"
}

TEST_FILES=(file_8m file_31m file_32m file_33m file_40m file_64m file_200m file_elf)

declare -A EXPECTED_SIZES=(
    ["file_8m"]=8388608
    ["file_31m"]=32505856
    ["file_32m"]=33554432
    ["file_33m"]=34603008
    ["file_40m"]=41943040
    ["file_64m"]=67108864
    ["file_200m"]=209715200
)

# Resolve file_elf size from workspace
felf="${WORKSPACE}/testfiles/file_elf"
if [[ -f "$felf" ]]; then
    EXPECTED_SIZES["file_elf"]=$(stat -c '%s' "$felf")
fi

declare -A EXPECTED_HASHES=()
HASH_VERIFIED_FILES=(file_32m file_elf)

# Compute hashes from existing test files
for f in "${HASH_VERIFIED_FILES[@]}"; do
    fp="${WORKSPACE}/testfiles/${f}"
    if [[ -f "$fp" ]]; then
        EXPECTED_HASHES["$f"]=$(sha256sum "$fp" | cut -d' ' -f1)
        log "hash: ${f} = ${EXPECTED_HASHES[$f]:0:16}…"
    fi
done

format_result() {
    local stage="$1"; shift
    local line="${stage}:"
    for token in "$@"; do
        local f="${token%%=*}" v="${token#*=}"
        local exp="${EXPECTED_SIZES[$f]:-}"
        if [[ -z "$exp" ]]; then
            line+=" ${f}=${v}(NO_EXPECTED)"
        elif [[ "$v" == "$exp" ]]; then
            line+=" ${f}=PASS"
        elif [[ "$v" =~ ^[0-9]+$ ]]; then
            line+=" ${f}=FAIL(exp=${exp},got=${v})"
        else
            line+=" ${f}=${v}"
        fi
    done
    echo "$line"
}

stage_b_measure() {
    local img="$1"
    local tokens=()
    for f in "${TEST_FILES[@]}"; do
        local sz; sz=$(debugfs_size "$img" "/testfiles/${f}")
        tokens+=("${f}=${sz}")
    done
    echo "${tokens[*]}"
}

stage_b_verify_hashes() {
    local img="$1"
    local hash_results=()
    local any_fail=0
    mkdir -p "$VERIFY_TMPDIR"
    for f in "${HASH_VERIFIED_FILES[@]}"; do
        local exp_hash="${EXPECTED_HASHES[$f]:-}"
        if [[ -z "$exp_hash" ]]; then
            hash_results+=("${f}=HASH_SKIP(no_expected)")
            continue
        fi
        local tmp_path="${VERIFY_TMPDIR}/${f}"
        debugfs -R "dump /testfiles/${f} ${tmp_path}" "$img" 2>/dev/null || {
            hash_results+=("${f}=HASH_DUMP_FAILED")
            any_fail=1
            continue
        }
        local got_hash
        got_hash=$(sha256sum "$tmp_path" 2>/dev/null | cut -d' ' -f1) || got_hash="SHA256_FAILED"
        rm -f "$tmp_path"
        if [[ "$got_hash" == "$exp_hash" ]]; then
            hash_results+=("${f}=HASH_PASS")
        else
            hash_results+=("${f}=HASH_FAIL(exp=${exp_hash:0:12}…,got=${got_hash:0:12}…)")
            any_fail=1
        fi
    done
    echo "${hash_results[*]}"
    return $any_fail
}

write_pressure_containerfile() {
    local ws="$1" slot="$2"
    local uid; uid="$(date +%s%N)-p3retry-${slot}"
    mkdir -p "${ws}/.nexus"
    cat > "${ws}/.nexus/Containerfile" << EOF
# repro: Phase 3 retry — pressure slot ${slot}
FROM debian:bookworm-slim
RUN echo "repro-p3retry-uid=${uid}" > /dev/null
COPY testfiles/ /testfiles/
RUN sha256sum /testfiles/file_32m /testfiles/file_elf > /testfiles/.HASHES || true
EOF
}

mkdir -p "$BUILD_LOG_DIR" "$VERIFY_TMPDIR"
trap 'rm -rf "$VERIFY_TMPDIR"' EXIT

log "=== Phase 3 retry: 3 concurrent builds ==="
log "buildkit.ext4 state:"
(debugfs -R "stats" ~/.local/state/nexus3/caches/buildkit.ext4 2>/dev/null \
    | grep -E "Block count|Free blocks" | tr '\n' ' ') || true
echo ""
log "Host disk:"
df -h / | tail -1

local_before=$(list_digests_sorted)
pids=()

for slot in A B C; do
    pws="${WORKSPACE}_pressure_${slot}"
    mkdir -p "${pws}/.nexus" "${pws}/testfiles"
    for f in "${TEST_FILES[@]}"; do
        _psrc="${WORKSPACE}/testfiles/${f}"
        _pdst="${pws}/testfiles/${f}"
        if [[ ! -f "$_pdst" ]] && [[ -f "$_psrc" ]]; then
            ln "$_psrc" "$_pdst" 2>/dev/null || cp "$_psrc" "$_pdst"
        fi
    done
    write_pressure_containerfile "$pws" "$slot"

    timeout "$BASH_BUILD_TIMEOUT" "$NEXUS3" create \
        "${REPRO_PROJECT}/p3retry-${slot}" \
        --file "$pws" \
        --no-user-mounts \
        > "${BUILD_LOG_DIR}/p3retry-${slot}.log" 2>&1 &
    pids+=($!)
    log "launched p3retry-${slot} (pid ${!})"
done

log "waiting for ${#pids[@]} concurrent builds..."
results=()
for pid in "${pids[@]}"; do
    wait "$pid" && results+=("pid=${pid}:EXIT0") || results+=("pid=${pid}:EXIT$?")
done
log "all concurrent builds finished: ${results[*]}"

local_after=$(list_digests_sorted)
new_digests=$(comm -13 <(echo "$local_before") <(echo "$local_after") || true)

pidx=0
any_fail=0
for nd in $new_digests; do
    (( pidx++ )) || true
    short="${nd#sha256:}"
    img="${IMAGE_STORE}/${short}/artifact"
    [[ ! -f "$img" ]] && { log "WARN: image missing: $img"; continue; }
    plabel="p3retry-img${pidx}"
    log "=== ${plabel}: Stage B — debugfs on ${short:0:16}... ==="
    raw_b=$(stage_b_measure "$img")
    # shellcheck disable=SC2086
    pb_line=$(format_result "Stage_B" $raw_b)
    log "$pb_line"
    if echo "$pb_line" | grep -qF "FAIL("; then
        log "TRUNCATION DETECTED in ${plabel}"
        any_fail=1
    fi
    hash_line=$(stage_b_verify_hashes "$img")
    log "  HashVerify: ${hash_line}"
    if echo "$hash_line" | grep -qF "HASH_FAIL"; then
        log "HASH MISMATCH in ${plabel}"
        any_fail=1
    fi
    # Agent-size check
    AGENT_BIN="$(command -v nexus3-agent 2>/dev/null || true)"
    if [[ -n "$AGENT_BIN" ]]; then
        host_size=$(stat -c '%s' "$AGENT_BIN" 2>/dev/null || echo 0)
        guest_size=$(debugfs_size "$img" "/sbin/nexus3-agent")
        if [[ "$guest_size" == "$host_size" ]]; then
            log "  AgentSize: PASS(${guest_size}B)"
        else
            log "  AgentSize: FAIL(exp=${host_size},got=${guest_size})"
            any_fail=1
        fi
    fi
done

log "Total new images captured: ${pidx}"
if (( pidx == 0 )); then
    log "RESULT: no images captured — all 3 concurrent builds failed"
elif (( any_fail )); then
    log "RESULT: TRUNCATION/HASH MISMATCH detected in Phase 3 retry"
else
    log "RESULT: all ${pidx} captured image(s) CLEAN — no truncation"
fi
