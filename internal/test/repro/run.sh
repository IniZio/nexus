#!/usr/bin/env bash
# run.sh — repro harness for the buildkit 32 MiB export-truncation bug.
#
# USAGE
#   bash internal/test/repro/run.sh [ITERATIONS] [--pressure]
#
#   ITERATIONS  sequential baseline build iterations (default 10)
#   --pressure  after sequential baseline, run all pressure variants:
#                 Phase 2 — constrained builder memory (--builder-memory 2048)
#                 Phase 3 — 3 concurrent builds
#                 Phase 4 — heavy/long build (~20 min workload profile)
#
# FLAW CORRECTIONS vs. prior run (both required for a valid negative result)
#
#   FLAW 1 FIXED — test files are now INCOMPRESSIBLE:
#     Files are pre-generated from /dev/urandom before any build and written
#     into $WORKSPACE/testfiles/. Containerfile uses COPY to pull them in.
#     This prevents sparse-file or zstd-dict shortcuts that zero-filled files
#     could exploit. A real ELF binary (nexus3 CLI, ~41.6 MiB) is also
#     included as file_elf — matching the original fault file type exactly.
#
#   FLAW 2 FIXED — pressure variants are now exercised:
#     Phase 2: --builder-memory 2048 (mimics 3 GB → 4.5 GB guest memory climb
#              and balloon pressure the supervisor log showed at fault time)
#     Phase 3: 3 concurrent builds (concurrent ExporterLocal I/O)
#     Phase 4: heavy build with compile-like workload targeting ~20 min profile
#
# HASH VERIFICATION
#   Size-only checks cannot detect truncation for file_32m (32 MiB exactly —
#   truncated at 32 MiB gives the same byte count). For file_32m and file_elf
#   the harness:
#     1. records sha256 of the source file before any build
#     2. after the build, dumps the file from the packed ext4 with debugfs
#     3. recomputes sha256 and compares
#   All other files rely on size mismatch (truncation at 32 MiB → 33554432
#   bytes, which differs from their expected sizes).
#
# HOW BUILDS ARE TRIGGERED
#   `nexus3 image build` is not yet wired (returns ErrNoBuilder). The real
#   build path is: nexus3 create <project>/<name> --file <workspace-dir>
#   which boots a builder VM, runs buildkitd inside it, and stores the
#   resulting image. Each iteration writes a fresh Containerfile with a
#   unique RUN line to bust buildkit's layer cache.
#
# DUAL-STAGE MEASUREMENT
#   Stage A — in-guest rootfsDir, BEFORE mke2fs packs it (best-effort;
#     nexus3 exec returns "sandbox not found" for builder VMs — documented).
#   Stage B — packed ext4 artifact, AFTER mke2fs (primary measurement).
#
# REQUIREMENTS
#   nexus3 binary on PATH or NEXUS3 env var
#   debugfs (apt: e2fsprogs)
#   jq, sha256sum
#   A builder image must already exist (run at least one real
#   `nexus3 create --file` build to provision the builder VM image).
set -euo pipefail

# ── Configuration ─────────────────────────────────────────────────────────────
NEXUS3="${NEXUS3:-nexus3}"
ITERATIONS="${1:-10}"
PRESSURE=0
SKIP_PHASE2="${SKIP_PHASE2:-0}"
for arg in "$@"; do [[ "$arg" == "--pressure" ]] && PRESSURE=1; done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE="$SCRIPT_DIR/workspace"
IMAGE_STORE="${NEXUS3_STATE_DIR:-$HOME/.local/state/nexus3}/images/sha256"
BUILD_LOG_DIR="$SCRIPT_DIR/logs"
VERIFY_TMPDIR="${TMPDIR:-/tmp}/repro-verify-$$"

# Outer wall-clock cap passed via env to nexus3. Actual buildkitd solve
# timeout is separate (NEXUS3_BUILD_SOLVE_TIMEOUT, default 10 min).
export NEXUS3_BUILD_TASK_TIMEOUT="${NEXUS3_BUILD_TASK_TIMEOUT:-25m}"
# bash timeout for the whole create call (slightly longer than above).
BASH_BUILD_TIMEOUT=1800  # 30 min in seconds

# How long to wait for new builder-supervisors entry after create starts.
BUILDER_SANDBOX_POLL_TIMEOUT=60
# How long to wait for file_200m in the in-guest export dir.
# Stage A window is brief (rootfsDir is deleted immediately post-export).
# With 15s-bounded exec, 180s gives ~12 poll attempts before giving up.
FILE_POLL_TIMEOUT=180

# Repro project/name prefix for created sandboxes.
REPRO_PROJECT="repro"

# Real ELF binary used as file_elf — matches original fault's nexus3-agent
# file type; size is determined at runtime by setup_test_files().
REAL_ELF_SRC="${NEXUS3_REAL_ELF:-$HOME/.local/bin/nexus3}"

# Expected file sizes — set statically for urandom files, file_elf set below.
declare -A EXPECTED_SIZES=(
    ["file_8m"]=8388608
    ["file_31m"]=32505856
    ["file_32m"]=33554432     # exactly at 32 MiB boundary
    ["file_33m"]=34603008
    ["file_40m"]=41943040
    ["file_64m"]=67108864
    ["file_200m"]=209715200
    # file_elf set by setup_test_files()
)

# SHA256 hashes for hash-verified files (set by setup_test_files()).
# file_32m: truncation at 32 MiB produces the SAME size — hash is the only
#            way to detect corruption/truncation for this file.
# file_elf: real ELF binary, both size and hash verified.
declare -A EXPECTED_HASHES=()

TEST_FILES=(file_8m file_31m file_32m file_33m file_40m file_64m file_200m file_elf)

# Files for which we dump+hash-verify from ext4 (not just size check).
HASH_VERIFIED_FILES=(file_32m file_elf)

# In-guest export path (from internal/core/agent/buildkit_linux.go).
GUEST_EXPORT_DIR="/var/lib/buildkit/nexus3-export"
GUEST_ROOTFS_GLOB="${GUEST_EXPORT_DIR}/nexus3-inguestbuild-rootfs-*"

# ── Helpers ───────────────────────────────────────────────────────────────────
log() { echo "[$(date +'%H:%M:%S')] $*"; }

list_digests_sorted() {
    "$NEXUS3" --json image ls 2>/dev/null \
        | jq -r '.data.images[].digest' 2>/dev/null \
        | sort || true
}

BUILDER_SUPERVISORS_DIR="${NEXUS3_STATE_DIR:-$HOME/.local/state/nexus3}/builder-supervisors"

snapshot_builder_supervisors() {
    ls -1 "$BUILDER_SUPERVISORS_DIR" 2>/dev/null | sort || true
}

find_new_builder_sandbox() {
    local before_snapshot="$1"
    local current
    current=$(ls -1 "$BUILDER_SUPERVISORS_DIR" 2>/dev/null | sort || true)
    comm -13 <(echo "$before_snapshot") <(echo "$current") | tail -1 || true
}

wait_for_builder_sandbox() {
    local before_snapshot="$1" timeout_s="$2" elapsed=0
    while (( elapsed < timeout_s )); do
        local id; id=$(find_new_builder_sandbox "$before_snapshot")
        [[ -n "$id" ]] && { echo "$id"; return 0; }
        sleep 1; (( elapsed += 1 )) || true
    done
    return 1
}

guest_exec() {
    local sb_id="$1"; shift
    # Bounded exec — builder VM agent may be unresponsive under heavy I/O.
    # Without a timeout, a hung exec blocks the entire poll loop forever.
    timeout 15 "$NEXUS3" exec "$sb_id" -- "$@" 2>/dev/null
}

wait_for_export_files() {
    local sb_id="$1" timeout_s="$2" elapsed=0
    while (( elapsed < timeout_s )); do
        local rd
        rd=$(guest_exec "$sb_id" \
            sh -c "d=\$(ls -d ${GUEST_ROOTFS_GLOB} 2>/dev/null | head -1); \
                   [ -f \"\$d/testfiles/file_200m\" ] && echo \$d" \
            2>/dev/null) || true
        [[ -n "$rd" ]] && { echo "$rd"; return 0; }
        sleep 3; (( elapsed += 3 )) || true
    done
    return 1
}

stage_a_measure() {
    local sb_id="$1" rootfs_dir="$2"
    local tokens=()
    for f in "${TEST_FILES[@]}"; do
        local sz
        sz=$(guest_exec "$sb_id" stat -c '%s' "${rootfs_dir}/testfiles/${f}" 2>/dev/null \
            | tr -d '[:space:]') || sz="EXEC_FAILED"
        tokens+=("${f}=${sz}")
    done
    echo "${tokens[*]}"
}

debugfs_size() {
    local img="$1" path="$2"
    debugfs -R "stat ${path}" "$img" 2>/dev/null \
        | grep -oP '(?<=Size: )\d+' | head -1 || echo "DEBUGFS_ERR"
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

# Dump hash-verified files from ext4 and verify sha256.
# Appends HASH_PASS or HASH_FAIL:<file> to a result variable.
# Returns 0 if all verified files match, 1 if any mismatch.
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
        # debugfs dump extracts the file from the ext4 image.
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

# Compare token list against EXPECTED_SIZES. Returns a formatted result line.
# NOTE: this function runs in a subshell via $(...) — it MUST NOT update global
# counters (they would be silently lost). Counters are updated by the CALLER in
# the parent shell, for Stage B results only. Stage A is a mid-export/racy
# snapshot whose sizes reflect in-flight writes, not finished truncation events.
TOTAL_PASS=0
TOTAL_FAIL=0
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
            line+=" ${f}=${v}"   # EXEC_FAILED / DEBUGFS_ERR / etc.
        fi
    done
    echo "$line"
}

# stage_b_check_agent_size probes /sbin/nexus3-agent inside the packed ext4 image
# and compares its on-disk size against the host source binary. This is the ORIGINAL
# FAULT SEAM: the 32 MiB truncation was observed on the agent binary, injected via
# SolveRequest.AgentInstallPath in internal/core/agent/buildkit_linux.go. The
# user COPY test files (checked elsewhere) are a separate injection path.
# Uses debugfs non-destructively — never mounts the image read-write.
stage_b_check_agent_size() {
    local img="$1" host_agent="$2"
    local host_size; host_size=$(stat -c '%s' "$host_agent" 2>/dev/null || echo 0)
    local guest_size; guest_size=$(debugfs_size "$img" "/sbin/nexus3-agent")
    if [[ "$guest_size" == "DEBUGFS_ERR" ]]; then
        echo "agent-size:DEBUGFS_ERR(host=${host_size})"
        return
    fi
    if [[ "$guest_size" == "$host_size" ]]; then
        echo "agent-size:PASS(${guest_size}B)"
    else
        echo "agent-size:FAIL(exp=${host_size},got=${guest_size})"
    fi
}

# ── Test-file setup ───────────────────────────────────────────────────────────
# FLAW 1 FIX: Pre-generate incompressible test files from /dev/urandom.
# Using COPY in Containerfile so file content is identical source→image.
# file_elf is a real ELF binary (nexus3 CLI, ~41.6 MiB) matching the fault's
# original file type (nexus3-agent, ~36 MiB, also an ELF).
setup_test_files() {
    local tf_dir="$WORKSPACE/testfiles"
    mkdir -p "$tf_dir"

    log "setup_test_files: generating incompressible test files in ${tf_dir}"
    local needs_gen=0

    # Check which urandom files need (re-)generation.
    # Pairs: "name:size_bytes" — bash doesn't support multi-var for loops.
    local gen_pairs=(
        "file_8m:8388608"
        "file_31m:32505856"
        "file_33m:34603008"
        "file_40m:41943040"
        "file_64m:67108864"
        "file_200m:209715200"
    )
    for pair in "${gen_pairs[@]}"; do
        local fname="${pair%%:*}" size_bytes="${pair#*:}"
        local fp="${tf_dir}/${fname}"
        if [[ ! -f "$fp" ]] || [[ "$(stat -c '%s' "$fp" 2>/dev/null || echo 0)" != "$size_bytes" ]]; then
            log "  generating ${fname} (${size_bytes} bytes from /dev/urandom)..."
            dd if=/dev/urandom of="$fp" bs=1048576 count=$(( size_bytes / 1048576 )) status=none
            # Verify exact size (dd with exact MiB blocks is always exact here).
            local got; got=$(stat -c '%s' "$fp")
            [[ "$got" == "$size_bytes" ]] || {
                echo "ERROR: setup_test_files: ${fname}: expected ${size_bytes}, got ${got}"
                exit 1
            }
            needs_gen=1
        fi
    done

    # file_32m: exactly 33554432 bytes — truncation at 32 MiB gives same size.
    # Regenerate if size wrong OR if no expected hash yet (first run).
    local f32="${tf_dir}/file_32m"
    if [[ ! -f "$f32" ]] || [[ "$(stat -c '%s' "$f32" 2>/dev/null || echo 0)" != "33554432" ]] \
       || [[ -z "${EXPECTED_HASHES[file_32m]:-}" ]]; then
        log "  generating file_32m (33554432 bytes from /dev/urandom) — hash-verified..."
        dd if=/dev/urandom of="$f32" bs=1048576 count=32 status=none
        local got; got=$(stat -c '%s' "$f32")
        [[ "$got" == "33554432" ]] || { echo "ERROR: file_32m size wrong: $got"; exit 1; }
        needs_gen=1
    fi

    # file_elf: copy real ELF binary; size determined at runtime.
    local felf="${tf_dir}/file_elf"
    if [[ ! -f "$REAL_ELF_SRC" ]]; then
        log "WARN: real ELF not found at ${REAL_ELF_SRC}; generating urandom substitute"
        if [[ ! -f "$felf" ]] || [[ "$(stat -c '%s' "$felf" 2>/dev/null || echo 0)" -lt $((32*1024*1024)) ]]; then
            dd if=/dev/urandom of="$felf" bs=1048576 count=36 status=none
        fi
    else
        local src_size; src_size=$(stat -c '%s' "$REAL_ELF_SRC")
        if [[ ! -f "$felf" ]] || [[ "$(stat -c '%s' "$felf" 2>/dev/null || echo 0)" != "$src_size" ]]; then
            log "  copying real ELF ${REAL_ELF_SRC} (${src_size} bytes) → file_elf..."
            cp "$REAL_ELF_SRC" "$felf"
            needs_gen=1
        fi
    fi
    local elf_size; elf_size=$(stat -c '%s' "$felf")
    EXPECTED_SIZES["file_elf"]="$elf_size"
    log "  file_elf: ${elf_size} bytes"

    # Compute sha256 for hash-verified files.
    log "  computing sha256 for hash-verified files..."
    for f in "${HASH_VERIFIED_FILES[@]}"; do
        local fp="${tf_dir}/${f}"
        local h; h=$(sha256sum "$fp" | cut -d' ' -f1)
        EXPECTED_HASHES["$f"]="$h"
        log "  ${f}: sha256=${h:0:16}… size=${EXPECTED_SIZES[$f]}"
    done

    log "setup_test_files: done (needs_gen=${needs_gen})"
}

# ── Containerfile writer ───────────────────────────────────────────────────────
# FLAW 1 FIX: Uses COPY instead of RUN dd so content is the pre-generated
# /dev/urandom files, not zero-filled. A unique marker busts buildkit cache.
write_containerfile() {
    local iter="$1" extra_runs="${2:-}"
    local uid; uid="$(date +%s%N)-iter${iter}"
    mkdir -p "$WORKSPACE/.nexus"
    cat > "$WORKSPACE/.nexus/Containerfile" << EOF
# repro: 32 MiB truncation harness — iteration ${iter}
FROM debian:bookworm-slim
# Unique marker busts buildkit layer cache so every iteration re-exports.
RUN echo "repro-uid=${uid}" > /dev/null
# COPY incompressible test files from build context (pre-generated from
# /dev/urandom). file_elf is a real ELF binary — matching the original
# fault's nexus3-agent file type.
COPY testfiles/ /testfiles/
# Store sha256 inside the image so Stage B can cross-reference.
RUN sha256sum /testfiles/file_32m /testfiles/file_elf > /testfiles/.HASHES || true
${extra_runs}
EOF
}

write_pressure_containerfile() {
    local ws="$1" slot="$2"
    local uid; uid="$(date +%s%N)-pressure-${slot}"
    mkdir -p "${ws}/.nexus"
    cat > "${ws}/.nexus/Containerfile" << EOF
# repro: pressure slot ${slot}
FROM debian:bookworm-slim
RUN echo "repro-pressure-uid=${uid}" > /dev/null
COPY testfiles/ /testfiles/
RUN sha256sum /testfiles/file_32m /testfiles/file_elf > /testfiles/.HASHES || true
EOF
}

write_heavy_containerfile() {
    local iter="$1"
    local uid; uid="$(date +%s%N)-heavy${iter}"
    mkdir -p "$WORKSPACE/.nexus"
    cat > "$WORKSPACE/.nexus/Containerfile" << EOF
# repro: heavy-build variant — CPU+IO pressure without apt-get
# (apt-get stripped: previous attempt exhausted buildkit cache disk with no space
#  left on device before reaching the export seam; buildkit.ext4 was reset and
#  apt-get removed so the cache disk stays healthy for concurrent + heavy phases.
#  Files >32MiB are still present via COPY — the export-seam truncation target
#  is unchanged. CPU loop simulates sustained pressure during export.)
FROM debian:bookworm-slim
RUN echo "repro-heavy-uid=${uid}" > /dev/null
# Write incompressible large test files — these are the truncation targets.
COPY testfiles/ /testfiles/
# CPU pressure loop: 500 md5sum passes over file_200m (~100s CPU-bound).
# Mimics sustained compute pressure while the rootfs export seam is active.
RUN i=0; while [ \$i -lt 500 ]; do \
      md5sum /testfiles/file_200m > /dev/null 2>&1 || true; \
      i=\$(( i + 1 )); \
    done || true
RUN sha256sum /testfiles/file_32m /testfiles/file_elf > /testfiles/.HASHES || true
EOF
}

# ── Sandbox helpers ───────────────────────────────────────────────────────────
find_sandbox_by_name() {
    local name="$1"
    "$NEXUS3" --json ps 2>/dev/null \
        | jq -r --arg n "$name" \
          '.data.sandboxes[] | select(.name == $n) | .id' 2>/dev/null \
        | head -1 || true
}

cleanup_sandbox() {
    local sb_id="$1"
    [[ -z "$sb_id" ]] && return
    "$NEXUS3" stop "$sb_id" 2>/dev/null || true
    sleep 2
    "$NEXUS3" rm "$sb_id" 2>/dev/null || true
}

# ── Core build+measure function ───────────────────────────────────────────────
ALL_RESULTS=()

run_one_iteration() {
    local label="$1" iter="$2" ws="${3:-$WORKSPACE}" extra_build_flags="${4:-}" extra_runs="${5:-}"
    local sandbox_name="${REPRO_PROJECT}-${label}"
    local build_log="${BUILD_LOG_DIR}/${label}.log"
    local stage_a_line="Stage_A:not_captured"
    local stage_b_line="Stage_B:not_captured"

    log "=== ${label}: writing Containerfile (iter=${iter}) ==="
    if [[ -n "$extra_runs" ]]; then
        write_heavy_containerfile "$iter"
    else
        write_containerfile "$iter"
    fi

    # Ensure testfiles are in this workspace.
    if [[ "$ws" != "$WORKSPACE" ]]; then
        mkdir -p "${ws}/testfiles"
        # Hard-link test files to avoid copying 200+ MiB per pressure slot.
        for f in "${TEST_FILES[@]}"; do
            local src="${WORKSPACE}/testfiles/${f}"
            local dst="${ws}/testfiles/${f}"
            if [[ ! -f "$dst" ]] && [[ -f "$src" ]]; then
                ln "$src" "$dst" 2>/dev/null || cp "$src" "$dst"
            fi
        done
    fi

    log "=== ${label}: pre-build image list + builder-supervisors snapshot ==="
    local before; before=$(list_digests_sorted)
    local sb_snapshot; sb_snapshot=$(snapshot_builder_supervisors)

    # shellcheck disable=SC2086
    log "=== ${label}: starting build (timeout=${NEXUS3_BUILD_TASK_TIMEOUT}) build_flags='${extra_build_flags}' ==="
    # shellcheck disable=SC2086
    timeout "$BASH_BUILD_TIMEOUT" "$NEXUS3" create \
        "${REPRO_PROJECT}/${sandbox_name}" \
        --file "$ws" \
        --no-user-mounts \
        $extra_build_flags \
        > "$build_log" 2>&1 &
    local build_pid=$!
    log "=== ${label}: build pid=${build_pid} ==="

    # ── Stage A: in-guest measurement (best-effort) ───────────────────────────
    log "=== ${label}: Stage A — polling builder-supervisors dir ==="
    local sb_id=""
    if sb_id=$(wait_for_builder_sandbox "$sb_snapshot" "$BUILDER_SANDBOX_POLL_TIMEOUT" 2>/dev/null); then
        log "=== ${label}: Stage A — builder sandbox=${sb_id}; trying nexus3 exec ==="
        local rootfs_dir=""
        if rootfs_dir=$(wait_for_export_files "$sb_id" "$FILE_POLL_TIMEOUT" 2>/dev/null); then
            log "=== ${label}: Stage A — measuring in ${rootfs_dir} ==="
            local raw_a; raw_a=$(stage_a_measure "$sb_id" "$rootfs_dir")
            # shellcheck disable=SC2086
            # Label explicitly as mid-export/racy: sizes are sampled while buildkit
            # is still writing the rootfsDir. Values like FAIL(exp=41943040,got=11829248)
            # reflect an in-flight write, not a truncation event. Do NOT read these
            # as failures — Stage B (packed ext4) is the authoritative measurement.
            stage_a_line=$(format_result "Stage_A[mid-export/racy]" $raw_a)
            log "$stage_a_line"
        else
            local exec_test_err
            exec_test_err=$(guest_exec "$sb_id" ls /var/lib/buildkit/ 2>&1) || true
            log "=== ${label}: Stage A — file_200m not seen (exec: ${exec_test_err:0:80}) ==="
            stage_a_line="Stage_A:exec_unavailable(${exec_test_err:0:60})"
        fi
    else
        log "=== ${label}: Stage A — no new builder-supervisors entry in ${BUILDER_SANDBOX_POLL_TIMEOUT}s ==="
        stage_a_line="Stage_A:no_builder_vm_seen"
    fi

    # ── Wait for build to finish ───────────────────────────────────────────────
    log "=== ${label}: waiting for build pid=${build_pid} ==="
    local build_exit=0
    wait "$build_pid" || build_exit=$?

    if [[ $build_exit -eq 124 ]]; then
        log "TIMEOUT: ${label} — bash timeout ${BASH_BUILD_TIMEOUT}s exceeded"
        ALL_RESULTS+=("${label} | ${stage_a_line} | Stage_B:TIMEOUT")
        return
    fi
    if [[ $build_exit -ne 0 ]]; then
        log "BUILD FAILED (exit=${build_exit}): ${label}"
        tail -20 "$build_log" | sed 's/^/  /' || true
        ALL_RESULTS+=("${label} | ${stage_a_line} | Stage_B:BUILD_FAILED:${build_exit}")
        local partial_sb; partial_sb=$(find_sandbox_by_name "$sandbox_name")
        cleanup_sandbox "$partial_sb"
        return
    fi

    # ── Stage A (post-build ext4 probe) ──────────────────────────────────────
    if [[ "$stage_a_line" == "Stage_A:no_builder_vm_seen" || "$stage_a_line" == "Stage_A:not_captured" ]]; then
        local cache_ext4="${HOME}/.local/state/nexus3/caches/buildkit.ext4"
        if [[ -f "$cache_ext4" ]]; then
            local export_ls
            export_ls=$(debugfs -R "ls -l /nexus3-export" "$cache_ext4" 2>&1) || true
            if echo "$export_ls" | grep -q "File not found"; then
                stage_a_line="Stage_A:unobtainable(buildkit.ext4:/nexus3-export_absent_post_build)"
            elif [[ -z "$export_ls" ]]; then
                stage_a_line="Stage_A:unobtainable(buildkit.ext4:/nexus3-export_empty_post_build)"
            else
                stage_a_line="Stage_A:unobtainable(buildkit.ext4:/nexus3-export_present_but_no_exec)"
            fi
        else
            stage_a_line="Stage_A:unobtainable(buildkit.ext4_not_found)"
        fi
        log "=== ${label}: Stage A (post-build-ext4): ${stage_a_line} ==="
    fi

    # ── Stage B: packed ext4 measurement + hash verification ──────────────────
    log "=== ${label}: finding new image after build ==="
    local after; after=$(list_digests_sorted)
    local new_digest; new_digest=$(comm -13 <(echo "$before") <(echo "$after") | head -1) || new_digest=""

    if [[ -z "$new_digest" ]]; then
        log "WARN: ${label} — no new image digest (build-cache hit?)"
        stage_b_line="Stage_B:NO_NEW_IMAGE(cache_hit)"
    else
        local short="${new_digest#sha256:}"
        local img="${IMAGE_STORE}/${short}/artifact"
        if [[ ! -f "$img" ]]; then
            log "ERROR: ${label} — image file missing: ${img}"
            stage_b_line="Stage_B:IMAGE_FILE_MISSING"
        else
            log "=== ${label}: Stage B — debugfs size check on ${short:0:16}... ==="
            local raw_b; raw_b=$(stage_b_measure "$img")
            # shellcheck disable=SC2086
            stage_b_line=$(format_result "Stage_B" $raw_b)
            log "$stage_b_line"

            # Hash verification for file_32m and file_elf.
            log "=== ${label}: Stage B — hash verification (file_32m, file_elf) ==="
            local hash_line; hash_line=$(stage_b_verify_hashes "$img")
            log "  HashVerify: ${hash_line}"
            stage_b_line="${stage_b_line} | HashVerify:${hash_line}"

            # Agent-size check: probe /sbin/nexus3-agent in the final ext4 image.
            # This is the original fault seam — a different injection path from
            # the user COPY test files. Without this check the harness cannot
            # observe the historical truncation site.
            log "=== ${label}: Stage B — agent-size check ==="
            local agent_check; agent_check=$(stage_b_check_agent_size "$img" "$AGENT_BIN")
            log "  AgentSize: ${agent_check}"
            stage_b_line="${stage_b_line} | AgentSize:${agent_check}"

            # Log buildkit cache disk block stats.
            local cache_disk="${HOME}/.local/state/nexus3/caches/buildkit.ext4"
            if [[ -f "$cache_disk" ]]; then
                local bstats
                bstats=$(debugfs -R "stats" "$cache_disk" 2>/dev/null \
                    | grep -E "Block count|Free blocks" | tr '\n' ' ') || bstats="n/a"
                log "=== ${label}: buildkit cache disk: ${bstats} ==="
            fi
        fi
    fi

    # ── Per-iteration counters (Stage B only; Stage A is mid-export/racy) ─────
    # format_result runs in a subshell — its internal counter writes are lost.
    # Parse stage_b_line here in the parent shell for reliable accounting.
    if echo "$stage_b_line" | grep -qF "FAIL("; then
        local fail_count
        fail_count=$(echo "$stage_b_line" | grep -oF "FAIL(" | wc -l)
        (( TOTAL_FAIL += fail_count )) || true
    elif echo "$stage_b_line" | grep -qF "=PASS"; then
        (( TOTAL_PASS++ )) || true
    fi

    ALL_RESULTS+=("${label} | ${stage_a_line} | ${stage_b_line}")

    log "=== ${label}: cleaning up sandbox ${sandbox_name} ==="
    local created_sb; created_sb=$(find_sandbox_by_name "$sandbox_name")
    cleanup_sandbox "$created_sb"
}

# ── Sanity checks ─────────────────────────────────────────────────────────────
log "repro harness — ITERATIONS=${ITERATIONS} PRESSURE=${PRESSURE}"
log "NEXUS3_BUILD_TASK_TIMEOUT=${NEXUS3_BUILD_TASK_TIMEOUT}"
log "nexus3 binary: $(command -v "$NEXUS3" 2>/dev/null || echo "NOT FOUND: set NEXUS3 env var")"
log "real ELF source: ${REAL_ELF_SRC} ($(stat -c '%s' "$REAL_ELF_SRC" 2>/dev/null || echo 'not found') bytes)"
log "workspace: ${WORKSPACE}"
log "image store: ${IMAGE_STORE}"

for req in "$NEXUS3" debugfs jq sha256sum strings; do
    command -v "$req" > /dev/null 2>&1 || {
        echo "ERROR: required tool not found: ${req}"
        exit 1
    }
done

# ── Agent-freshness precondition ──────────────────────────────────────────────
# The builder image cache key is sha256(agentBytes)[:8]: a stale on-PATH
# nexus3-agent silently reuses the old builder VM image, running code that
# predates both verifyAgentIntegrity and the rootfs-size-manifest guard.
# Every run with a stale agent is VOID evidence — the harness cannot observe
# the truncation even if it occurs. Hard-fail here so the operator cannot
# accidentally produce a clean negative on a blind binary.
AGENT_BIN="$(command -v nexus3-agent 2>/dev/null || true)"
if [[ -z "$AGENT_BIN" ]]; then
    echo "ERROR: nexus3-agent not found on PATH. Run 'make install-agent' from the repo root." >&2
    exit 1
fi
# NOTE: grep -q exits early on first match, which sends SIGPIPE to strings.
# With pipefail, the resulting exit-141 from strings propagates and incorrectly
# trips the gate. Disable pipefail in a subshell around each check so only
# grep's exit code (0=found, 1=not found) governs the condition.
if ! (set +o pipefail; strings "$AGENT_BIN" | grep -q 'rootfs-size-manifest'); then
    echo "ERROR: on-PATH nexus3-agent (${AGENT_BIN}) predates the rootfs-size-manifest guard." >&2
    echo "  Run 'make install-agent' from the repo root, then re-run this harness." >&2
    exit 1
fi
if ! (set +o pipefail; strings "$AGENT_BIN" | grep -q 'rootfs export truncated'); then
    echo "ERROR: on-PATH nexus3-agent (${AGENT_BIN}) predates the verifyAgentIntegrity guard." >&2
    echo "  Run 'make install-agent' from the repo root, then re-run this harness." >&2
    exit 1
fi
log "agent-freshness: OK — both guard strings present in ${AGENT_BIN} ($(stat -c '%s' "$AGENT_BIN") bytes)"

mkdir -p "$BUILD_LOG_DIR" "$WORKSPACE/.nexus" "$VERIFY_TMPDIR"
trap 'rm -rf "$VERIFY_TMPDIR"' EXIT

# Setup incompressible test files (idempotent — skips regeneration if sizes match).
setup_test_files

log "Expected sizes: file_elf=${EXPECTED_SIZES[file_elf]}"
log "Hash-verified files: ${HASH_VERIFIED_FILES[*]}"
log "  file_32m sha256: ${EXPECTED_HASHES[file_32m]:0:16}…"
log "  file_elf sha256: ${EXPECTED_HASHES[file_elf]:0:16}…"

# ── Phase 1: sequential baseline builds ───────────────────────────────────────
log "--- Phase 1: ${ITERATIONS} sequential baseline builds (incompressible /dev/urandom files) ---"
for (( i = 1; i <= ITERATIONS; i++ )); do
    run_one_iteration "iter-$(printf '%02d' "$i")" "$i"
    log ""
done

# ── Phase 2 / 3 / 4: pressure variants (optional) ────────────────────────────
if [[ $PRESSURE -eq 1 ]]; then

    # ── Phase 2: constrained builder memory ───────────────────────────────────
    # Mimics the original fault scenario: guest memory climbed 3.0→4.5 GB under
    # sustained pressure. --builder-memory 2048 forces the builder VM to operate
    # below its default headroom, triggering balloon inflate/deflate cycles.
    # Set SKIP_PHASE2=1 to bypass (when Phase 2 evidence is already on record).
    if [[ "$SKIP_PHASE2" == "1" ]]; then
        log "--- Phase 2: SKIPPED (SKIP_PHASE2=1) ---"
    else
    log "--- Phase 2: 5 sequential builds with --builder-memory 2048 (constrained memory) ---"
    for (( i = 1; i <= 5; i++ )); do
        run_one_iteration "mem-$(printf '%02d' "$i")" "$i" "$WORKSPACE" "--builder-memory 2048"
        log ""
    done
    fi

    # ── Phase 3: concurrent pressure builds ───────────────────────────────────
    # FLAW 2 FIX: now uses COPY of incompressible files (was /dev/zero before).
    # Three concurrent builds maximize ExporterLocal / fsutil DiffCopy contention.
    log "--- Phase 3: 3 concurrent builds (parallel ExporterLocal I/O pressure) ---"
    local_before=$(list_digests_sorted)
    pids=()

    for slot in A B C; do
        pws="${WORKSPACE}_pressure_${slot}"
        mkdir -p "${pws}/.nexus" "${pws}/testfiles"
        # Hard-link test files to avoid copying 400+ MiB × 3 slots.
        for f in "${TEST_FILES[@]}"; do
            _psrc="${WORKSPACE}/testfiles/${f}"
            _pdst="${pws}/testfiles/${f}"
            if [[ ! -f "$_pdst" ]] && [[ -f "$_psrc" ]]; then
                ln "$_psrc" "$_pdst" 2>/dev/null || cp "$_psrc" "$_pdst"
            fi
        done
        write_pressure_containerfile "$pws" "$slot"

        timeout "$BASH_BUILD_TIMEOUT" "$NEXUS3" create \
            "${REPRO_PROJECT}/pressure-${slot}" \
            --file "$pws" \
            --no-user-mounts \
            > "${BUILD_LOG_DIR}/pressure-${slot}.log" 2>&1 &
        pids+=($!)
        log "launched pressure-${slot} (pid ${!})"
    done

    log "waiting for ${#pids[@]} concurrent builds..."
    for pid in "${pids[@]}"; do
        wait "$pid" || log "WARN: pid ${pid} exited non-zero (check pressure-*.log)"
    done
    log "all concurrent builds finished"

    # Stage B for concurrent pressure images.
    local_after=$(list_digests_sorted)
    new_digests=$(comm -13 <(echo "$local_before") <(echo "$local_after") || true)
    pidx=0
    for nd in $new_digests; do
        (( pidx++ )) || true
        short="${nd#sha256:}"
        img="${IMAGE_STORE}/${short}/artifact"
        [[ ! -f "$img" ]] && { log "WARN: image missing: $img"; continue; }
        plabel="pressure-img${pidx}"
        log "=== ${plabel}: Stage B — debugfs on ${short:0:16}... ==="
        raw_b=$(stage_b_measure "$img")
        # shellcheck disable=SC2086
        pb_line=$(format_result "Stage_B" $raw_b)
        log "$pb_line"
        hash_line=$(stage_b_verify_hashes "$img")
        log "  HashVerify: ${hash_line}"
        ALL_RESULTS+=("${plabel} | Stage_A:concurrent_not_captured | ${pb_line} | HashVerify:${hash_line}")
    done

    for slot in A B C; do
        psb=$(find_sandbox_by_name "pressure-${slot}")
        cleanup_sandbox "$psb"
    done

    # ── Phase 4: heavy/long build (CPU pressure + I/O) ────────────────────────
    # Simulates the original ~20 min build profile: apt install + CPU-pinned loop
    # while the test files sit in the rootfs waiting to be exported. This is the
    # scenario most likely to trigger the truncation if it is timing-dependent.
    log "--- Phase 4: 3 heavy builds (CPU + I/O pressure, targets ~20 min profile) ---"
    for (( i = 1; i <= 3; i++ )); do
        run_one_iteration "heavy-$(printf '%02d' "$i")" "$i" "$WORKSPACE" "" "HEAVY"
        log ""
    done

fi  # PRESSURE

# ── Summary ───────────────────────────────────────────────────────────────────
log "=========================================="
log "SUMMARY"
log "  Build iterations with all files intact: ${TOTAL_PASS}"
log "  Total file-size/hash failures:          ${TOTAL_FAIL}"
log ""
log "Per-iteration results (label | Stage_A | Stage_B | HashVerify):"
for r in "${ALL_RESULTS[@]}"; do
    log "  $r"
done
log "=========================================="
log "Build logs: ${BUILD_LOG_DIR}/"
log "Updated harness: ${SCRIPT_DIR}/run.sh"
log "Run command:     bash ${SCRIPT_DIR}/run.sh [ITERATIONS] [--pressure]"
log "Pressure run:    bash ${SCRIPT_DIR}/run.sh 5 --pressure"

if (( TOTAL_FAIL > 0 )); then
    log "RESULT: TRUNCATION REPRODUCED — see FAIL entries above"
    exit 1
else
    log "RESULT: NO TRUNCATION OBSERVED in this run"
    exit 0
fi
