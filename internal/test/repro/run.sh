#!/usr/bin/env bash
# run.sh — repro harness for the buildkit 32 MiB export-truncation bug.
#
# USAGE
#   bash internal/test/repro/run.sh [ITERATIONS] [--pressure] [--disk-pressure] [--fallback-branch]
#
#   ITERATIONS       sequential baseline build iterations (default 10)
#   --pressure       after sequential baseline, run all pressure variants:
#                      Phase 2 — constrained builder memory (--builder-memory 2048)
#                      Phase 3 — 3 concurrent builds
#                      Phase 4 — heavy/long build (~20 min workload profile)
#   --disk-pressure  Phase 5 — export-scratch free-space exhaustion (CONDITION 1 / AC-2):
#                      fills buildkit.ext4 to 800/400/100 MiB free via debugfs,
#                      runs builds, observes whether buildkit silently truncates
#                      or propagates ENOSPC. Restores ext4 after each level.
#   --fallback-branch Phase 6 — exportBase="" fallback branch (CONDITION 2 / AC-2):
#                      injects /nexus3-export as a regular file in buildkit.ext4
#                      so os.MkdirAll fails and the export lands on /tmp instead.
#                      Restores ext4 after all builds.
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
DISK_PRESSURE=0
FALLBACK_BRANCH=0
SKIP_PHASE2="${SKIP_PHASE2:-0}"
for arg in "$@"; do
    [[ "$arg" == "--pressure" ]] && PRESSURE=1
    [[ "$arg" == "--disk-pressure" ]] && DISK_PRESSURE=1
    [[ "$arg" == "--fallback-branch" ]] && FALLBACK_BRANCH=1
done

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

# emit_fail CODE [key=value ...]
# Always produces HARNESS_INTEGRITY_FAIL(CODE,...) — the name structurally
# guarantees the mandatory FAIL( substring is present. Every integrity/failure
# token must go through this helper so a future edit cannot accidentally omit FAIL(.
emit_fail() {
    local code="$1"; shift
    echo "HARNESS_INTEGRITY_FAIL(${code}${*:+,$*})"
}

list_digests_sorted() {
    # FIX 4(c): Capture nexus3 exit code separately so a command failure
    # returns a sentinel instead of empty string. Empty string is silently
    # coerced to "no images" by comm -13, turning every pre-existing digest
    # into a "new" one — before-only-dead is NOT caught by NO_NEW_IMAGE.
    local _out _ec=0
    _out=$("$NEXUS3" --json image ls 2>/dev/null) || _ec=$?
    if [[ $_ec -ne 0 ]]; then
        echo "LIST_DIGESTS_FAILED"
        return 0
    fi
    echo "$_out" | jq -r '.data.images[].digest' 2>/dev/null | sort || true
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


debugfs_size() {
    local img="$1" path="$2"
    local _out
    _out=$(debugfs -R "stat ${path}" "$img" 2>/dev/null \
        | grep -oP '(?<=Size: )\d+' | head -1)
    if [[ -z "$_out" ]]; then echo "DEBUGFS_ERR"; else echo "$_out"; fi
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
            hash_results+=("${f}=$(emit_fail "no_expected_hash,file=${f}")")
            any_fail=1
            continue
        fi
        local tmp_path="${VERIFY_TMPDIR}/${f}"
        # debugfs dump extracts the file from the ext4 image.
        # NOTE: debugfs is fail-open — it exits 0 even for a missing path and
        # creates no output file. Never trust the exit code alone.
        debugfs -R "dump /testfiles/${f} ${tmp_path}" "$img" 2>/dev/null || {
            hash_results+=("${f}=$(emit_fail "dump_cmd_fail,file=${f}")")
            any_fail=1
            continue
        }
        # Confirm the dump produced a non-empty file. A missing path exits 0 and
        # creates nothing — that is a harness integrity failure, not truncation evidence.
        if [[ ! -s "$tmp_path" ]]; then
            rm -f "$tmp_path"
            hash_results+=("${f}=$(emit_fail "dump_no_output,file=${f}")")
            any_fail=1
            continue
        fi
        local got_hash
        got_hash=$(sha256sum "$tmp_path" 2>/dev/null | cut -d' ' -f1) || got_hash=""
        rm -f "$tmp_path"
        # A real sha256 digest is exactly 64 lowercase hex characters. Anything
        # else (empty string, truncated output) is a harness integrity failure —
        # route through emit_fail, never count as truncation evidence.
        if [[ ! "$got_hash" =~ ^[0-9a-f]{64}$ ]]; then
            hash_results+=("${f}=$(emit_fail "sha256_failed,file=${f},got=${got_hash:0:12}")")
            any_fail=1
            continue
        fi
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
# the parent shell for both Stage A (manifest) and Stage B results. Stage A feeds
# TOTAL_TRUNC via count_trunc_evidence; the manifest is written synchronously by
# the agent after Solve() completes and is not a racy snapshot.
TOTAL_PASS=0
TOTAL_FAIL=0
TOTAL_TRUNC=0     # Genuine truncation evidence only (TRUNCATED_AT_32MiB + real size mismatches)
TOTAL_PASS_P3=0   # Phase 3 Stage-B-only passes (Stage-A never captured in concurrent path)

# count_trunc_evidence: count genuine truncation tokens in a result line.
# Counts TRUNCATED_AT_32MiB + FAIL(exp=N,got=M) + HASH_FAIL( (content-hash mismatch).
# HASH_FAIL( is the sole detector for file_32m (whose correct size equals the truncation
# boundary — a size check alone cannot distinguish truncation from intact content).
# HASH_FAIL( is emitted ONLY when got_hash is a valid 64-hex-char sha256 digest that
# differs from expected. Dead probes (missing dump output, sha256 failures) are routed
# through emit_fail and produce HARNESS_INTEGRITY_FAIL(, never counted here.
# Does NOT count HARNESS_INTEGRITY_FAIL( tokens.
count_trunc_evidence() {
    local line="$1"
    local t m h
    t=$(echo "$line" | grep -oF 'TRUNCATED_AT_32MiB' | wc -l)
    m=$(echo "$line" | grep -oE 'FAIL\(exp=[0-9]+,got=[0-9]+' | wc -l)
    h=$(echo "$line" | grep -oF 'HASH_FAIL(' | wc -l)
    echo $(( t + m + h ))
}
format_result() {
    local stage="$1"; shift
    local line="${stage}:"
    for token in "$@"; do
        local f="${token%%=*}" v="${token#*=}"
        local exp="${EXPECTED_SIZES[$f]:-}"
        if [[ -z "$exp" ]]; then
            line+=" ${f}=$(emit_fail "no_expected,file=${f},val=${v}")"
        elif [[ "$v" == "$exp" ]]; then
            line+=" ${f}=PASS"
        elif [[ "$v" =~ ^[0-9]+$ ]]; then
            line+=" ${f}=FAIL(exp=${exp},got=${v})"
        else
            line+=" ${f}=$(emit_fail "probe_dead,file=${f},val=${v}")"
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
    # Fix 5: host_size==0 means the reference binary is absent. Do NOT compare
    # against zero — that would emit FAIL(exp=0,got=N) and falsely trigger
    # "TRUNCATION REPRODUCED" when the harness itself is misconfigured.
    if [[ "$host_size" -eq 0 ]]; then
        echo "agent-size:$(emit_fail "no_host_ref,agent=${host_agent}")"
        return
    fi
    local guest_size; guest_size=$(debugfs_size "$img" "/sbin/nexus3-agent")
    if [[ "$guest_size" == "DEBUGFS_ERR" ]]; then
        echo "agent-size:$(emit_fail "absent,path=/sbin/nexus3-agent,host=${host_size}")"
        return
    fi
    if [[ "$guest_size" == "$host_size" ]]; then
        echo "agent-size:PASS(${guest_size}B)"
    else
        echo "agent-size:FAIL(exp=${host_size},got=${guest_size})"
    fi
}

# stage_b_check_run_produced_size probes /usr/local/bin/run-produced-40m —
# a synthetic 40 MiB file created by "RUN dd" in the Containerfile.
# This is the DEFECT-1 FIX: a RUN-produced >32MiB file whose size is known
# exactly, making truncation unambiguously detectable in Stage B.
stage_b_check_run_produced_size() {
    local img="$1"
    local expected=41943040  # 40 * 1048576
    local got; got=$(debugfs_size "$img" "/usr/local/bin/run-produced-40m")
    if [[ "$got" == "DEBUGFS_ERR" ]]; then
        echo "run-produced-40m:$(emit_fail "absent,path=/usr/local/bin/run-produced-40m")"
        return
    fi
    if [[ "$got" == "$expected" ]]; then
        echo "run-produced-40m:PASS(${got}B)"
    elif [[ "$got" == "33554432" ]]; then
        echo "run-produced-40m:FAIL(TRUNCATED_AT_32MiB,exp=${expected},got=${got})"
    else
        echo "run-produced-40m:FAIL(exp=${expected},got=${got})"
    fi
}

# stage_b_check_dc_size probes the docker-compose-v2 binary installed by apt.
# Truncation to exactly 33554432 is the signature of the original bug.
# The expected size is unknown ahead of time (version-dependent), so only the
# truncation sentinel is checked definitively.
stage_b_check_dc_size() {
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
        # probe_dead: debugfs_size returned neither DEBUGFS_ERR nor a number —
        # the probe is dead and must never silently count as OK.
        echo "docker-compose:$(emit_fail "probe_dead,dc_path=${dc_path},val=${got}")"
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
        # FIX 4(a): Guard the EXPECTED side with the same ^[0-9a-f]{64}$ check
        # that :220 applies to the observed side. A malformed expected hash would
        # make every valid got_hash mismatch → HASH_FAIL( → counted as truncation
        # evidence → fabricated "TRUNCATION REPRODUCED". Leave the hash unset so
        # stage_b_verify_hashes' no_expected_hash guard fires (HARNESS_INTEGRITY_FAIL),
        # not HASH_FAIL.
        if [[ ! "$h" =~ ^[0-9a-f]{64}$ ]]; then
            log "  ${f}: sha256sum returned invalid digest '${h:0:12}' — expected hash NOT stored"
            continue
        fi
        EXPECTED_HASHES["$f"]="$h"
        log "  ${f}: sha256=${h:0:16}… size=${EXPECTED_SIZES[$f]}"
    done

    log "setup_test_files: done (needs_gen=${needs_gen})"
}

# ── Containerfile writer ───────────────────────────────────────────────────────
# DEFECT-1 FIX (restored apt-get RUN step): the original incident truncated
# docker-compose, written by runc into the overlay upper dir during a RUN step.
# A COPY-only Containerfile cannot reach that write path. apt-get is placed
# AFTER the uid marker so it is a genuine cache miss every iteration, exercising
# the runc->overlay write path each time. The cache disk is grown to 20 GiB
# (ensure_buildkit_disk_size) to accommodate repeated apt layers.
write_containerfile() {
    local iter="$1" extra_runs="${2:-}"
    local uid; uid="$(date +%s%N)-iter${iter}"
    mkdir -p "$WORKSPACE/.nexus"
    cat > "$WORKSPACE/.nexus/Containerfile" << EOF
# repro: 32 MiB truncation harness — iteration ${iter}
FROM ubuntu:24.04
# Unique marker: BEFORE apt-get so apt layers are also genuine cache misses,
# exercising the runc->overlay snapshotter write path on every iteration.
RUN echo "repro-uid=${uid}" > /dev/null
# DEFECT-1 FIX: install docker-compose-v2 via apt (faithful reproduction --
# original incident truncated docker-compose, an apt-installed binary written
# by runc into the snapshot upper dir, NOT by COPY from build context).
RUN echo "=== apt: installing docker-compose-v2 ===" && \
    DEBIAN_FRONTEND=noninteractive apt-get update 2>&1 && \
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends docker-compose-v2 2>&1 && \
    rm -rf /var/lib/apt/lists/*
# Guaranteed >32MiB RUN-produced truncation target: docker-compose-v2 from
# ubuntu:24.04 may be <32MiB (version-dependent). This dd file ensures
# at least one RUN-produced file crosses the 32 MiB boundary each iteration.
RUN dd if=/dev/urandom of=/usr/local/bin/run-produced-40m bs=1M count=40 2>/dev/null
# COPY incompressible test files from build context (pre-generated from
# /dev/urandom). file_elf is a real ELF binary -- matching the original
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
FROM ubuntu:24.04
RUN echo "repro-pressure-uid=${uid}" > /dev/null
RUN echo "=== apt: installing docker-compose-v2 ===" && \
    DEBIAN_FRONTEND=noninteractive apt-get update 2>&1 && \
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends docker-compose-v2 2>&1 && \
    rm -rf /var/lib/apt/lists/*
RUN dd if=/dev/urandom of=/usr/local/bin/run-produced-40m bs=1M count=40 2>/dev/null
COPY testfiles/ /testfiles/
RUN sha256sum /testfiles/file_32m /testfiles/file_elf > /testfiles/.HASHES || true
EOF
}

write_heavy_containerfile() {
    local iter="$1"
    local uid; uid="$(date +%s%N)-heavy${iter}"
    mkdir -p "$WORKSPACE/.nexus"
    cat > "$WORKSPACE/.nexus/Containerfile" << EOF
# repro: heavy-build variant -- CPU+IO pressure with apt-get restored
# (DEFECT-1 FIX: apt-get was previously stripped because it exhausted the
#  buildkit cache disk; the cache disk is now grown to 20 GiB to accommodate
#  repeated apt layers -- see ensure_buildkit_disk_size.)
FROM ubuntu:24.04
RUN echo "repro-heavy-uid=${uid}" > /dev/null
RUN echo "=== apt: installing docker-compose-v2 ===" && \
    DEBIAN_FRONTEND=noninteractive apt-get update 2>&1 && \
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends docker-compose-v2 2>&1 && \
    rm -rf /var/lib/apt/lists/*
RUN dd if=/dev/urandom of=/usr/local/bin/run-produced-40m bs=1M count=40 2>/dev/null
# Write incompressible large test files -- these are the truncation targets.
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

# ── Disk-pressure helpers (Phase 5) ──────────────────────────────────────────
# Writes a dense fill file into buildkit.ext4 via debugfs so the guest sees
# only target_free_mb of free space at /var/lib/buildkit. No root required —
# debugfs opens the image file as the owning user.
#
# The fill file is written to /PRESSURE_FILL in the ext4 root. Multiple calls
# overwrite the previous fill (rm first). Restore via restore_ext4_pressure.
CACHE_EXT4="${NEXUS3_STATE_DIR:-$HOME/.local/state/nexus3}/caches/buildkit.ext4"

# repair_ext4: run e2fsck to fix dirty/inconsistent state left by a builder VM
# that was killed without cleanly unmounting the filesystem. Must be called
# before any debugfs manipulation when a VM has recently used the ext4.
# e2fsck exit codes: 0=clean, 1/2=errors corrected, 4+=uncorrected.
repair_ext4() {
    local out ec
    log "repair_ext4: running e2fsck -p -f on buildkit.ext4..."
    out=$(e2fsck -p -f "$CACHE_EXT4" 2>&1) && ec=$? || ec=$?
    log "repair_ext4: e2fsck exit=${ec}: ${out:0:120}"
    if (( ec >= 4 )); then
        log "repair_ext4: WARN uncorrected errors (ec=${ec}) — debugfs may still fail"
        return 1
    fi
    return 0
}

ext4_free_mb() {
    local _out
    _out=$(debugfs -R "stats" "$CACHE_EXT4" 2>/dev/null \
        | grep "Free blocks" | grep -oP '\d+' | head -1)
    if [[ -z "$_out" ]]; then echo "DEBUGFS_ERR"; else echo $(( _out * 4096 / 1024 / 1024 )); fi
}

# ensure_buildkit_disk_size MIN_GIB
# Grows buildkit.ext4 to at least MIN_GIB using truncate + resize2fs if the
# current image is smaller. The image is sparse — truncate costs no additional
# host disk space until new blocks are actually written. Must be called while
# no builder VM is running (ext4 must not be mounted).
# DEFECT-1 FIX: apt-get install layers exhausted the original 10 GiB disk;
# growing to 20 GiB provides ~10 GiB headroom for repeated apt layers across
# multiple sweep iterations.
ensure_buildkit_disk_size() {
    local min_gib="$1"
    local min_bytes=$(( min_gib * 1024 * 1024 * 1024 ))
    if [[ ! -f "$CACHE_EXT4" ]]; then
        log "ensure_buildkit_disk_size: buildkit.ext4 not present -- skip (created on first nexus3 build)"
        return 0
    fi
    local current_bytes; current_bytes=$(stat -c '%s' "$CACHE_EXT4" 2>/dev/null || echo 0)
    if (( current_bytes >= min_bytes )); then
        log "ensure_buildkit_disk_size: size=${current_bytes}B >= ${min_gib}GiB target -- skip"
        return 0
    fi
    log "ensure_buildkit_disk_size: growing buildkit.ext4 from ${current_bytes}B to ${min_gib}GiB..."
    truncate -s "${min_gib}G" "$CACHE_EXT4"
    local ec=0
    e2fsck -f -p "$CACHE_EXT4" 2>/dev/null || ec=$?
    # e2fsck exits: 0=clean, 1=corrected, 2=corrected+reboot; 4+=uncorrected errors.
    if (( ec >= 4 )); then
        log "ensure_buildkit_disk_size: e2fsck exit=${ec} -- WARN: uncorrected errors; resize2fs skipped"
        return 1
    fi
    resize2fs "$CACHE_EXT4" 2>/dev/null || {
        log "ensure_buildkit_disk_size: resize2fs failed"
        return 1
    }
    local new_bytes; new_bytes=$(stat -c '%s' "$CACHE_EXT4")
    local free_mb; free_mb=$(ext4_free_mb)
    log "ensure_buildkit_disk_size: done -- new size=${new_bytes}B, ext4 free=${free_mb}MiB"
}

# fill_ext4_pressure target_free_mb host_tmp_fill_path
# Returns 0 if fill written, 1 if already at/below target or write failed.
# fill_ext4_pressure target_free_mb host_tmp_fill_path
# Returns 0 if fill written, 1 if already at/below target (benign skip),
# 2 if a hard error occurred (dead probe / repair fail / write fail);
# exit code 2 callers: TOTAL_FAIL already incremented inside this function.
fill_ext4_pressure() {
    local target_free_mb="$1" fill_path="$2"
    # Repair dirty state left by the previous builder VM before reading stats.
    repair_ext4 || {
        local _rtok; _rtok=$(emit_fail "REPAIR_FAILED,fill,img=${CACHE_EXT4}")
        log "fill_ext4_pressure: repair_ext4 failed — aborting fill — ${_rtok}"
        (( TOTAL_FAIL++ )) || true
        return 2
    }
    local free_mb; free_mb=$(ext4_free_mb)
    if [[ "$free_mb" == "DEBUGFS_ERR" ]]; then
        local _tok; _tok=$(emit_fail "FREESPACE_PROBE_DEAD,fill,img=${CACHE_EXT4}")
        log "fill_ext4_pressure: dead free-space probe — ${_tok}"
        (( TOTAL_FAIL++ )) || true
        return 2
    fi
    local fill_mb=$(( free_mb - target_free_mb ))
    if (( fill_mb <= 0 )); then
        log "fill_ext4_pressure: already at/below target (free=${free_mb} MiB, target=${target_free_mb} MiB)"
        return 1
    fi
    log "fill_ext4_pressure: free=${free_mb} MiB → leaving ${target_free_mb} MiB free; fill=${fill_mb} MiB"
    # Defensive: remove any prior /PRESSURE_FILL before writing the new one.
    debugfs -w "$CACHE_EXT4" -R "rm /PRESSURE_FILL" 2>&1 || true
    # IMPORTANT: must use /dev/urandom (not /dev/zero). debugfs writes zero-data
    # files as sparse (no block allocation), so all-zeros fill consumes no
    # ext4 blocks and the free-space test condition is never reached.
    log "fill_ext4_pressure: creating ${fill_mb} MiB fill file (urandom — sparse-proof)..."
    dd if=/dev/urandom of="$fill_path" bs=1048576 count="$fill_mb" status=none
    log "fill_ext4_pressure: writing fill into buildkit.ext4 (debugfs) ..."
    local dout
    if ! dout=$(debugfs -w "$CACHE_EXT4" -R "write $fill_path /PRESSURE_FILL" 2>&1); then
        local _wtok; _wtok=$(emit_fail "FILL_WRITE_FAILED,img=${CACHE_EXT4}")
        log "fill_ext4_pressure: debugfs write failed: ${dout} — ${_wtok}"
        (( TOTAL_FAIL++ )) || true
        rm -f "$fill_path"
        return 2
    fi
    rm -f "$fill_path"
    local new_free_mb; new_free_mb=$(ext4_free_mb)
    if [[ "$new_free_mb" == "DEBUGFS_ERR" ]]; then
        log "fill_ext4_pressure: done — post-fill free-space probe dead (fill write succeeded)"
    else
        log "fill_ext4_pressure: done — ext4 free now ${new_free_mb} MiB (target ${target_free_mb} MiB)"
    fi
    return 0
}

restore_ext4_pressure() {
    repair_ext4 || true  # best-effort; debugfs rm may still work
    log "restore_ext4_pressure: removing /PRESSURE_FILL from buildkit.ext4 ..."
    debugfs -w "$CACHE_EXT4" -R "rm /PRESSURE_FILL" 2>&1 || true
    local free_mb; free_mb=$(ext4_free_mb)
    if [[ "$free_mb" == "DEBUGFS_ERR" ]]; then
        local _tok; _tok=$(emit_fail "FREESPACE_PROBE_DEAD,restore,img=${CACHE_EXT4}")
        log "restore_ext4_pressure: dead free-space probe — ${_tok}"
        (( TOTAL_FAIL++ )) || true
    else
        log "restore_ext4_pressure: restored — ext4 free now ${free_mb} MiB"
    fi
}

# ── Fallback-branch helpers (Phase 6) ────────────────────────────────────────
# Injects a regular FILE named /nexus3-export into buildkit.ext4 so that when
# the guest tries os.MkdirAll("/var/lib/buildkit/nexus3-export", 0o700) it
# gets ENOTDIR and falls back to os.TempDir() (/tmp on the guest rootfs).
inject_nexus_export_blocker() {
    repair_ext4 || { log "inject_nexus_export_blocker: repair_ext4 failed — aborting"; return 1; }
    # Remove any pre-existing /nexus3-export (file OR directory). debugfs rm
    # refuses to remove a non-empty directory; use unlink instead — it removes
    # the directory entry from the parent regardless of contents (orphans the
    # inode). A subsequent e2fsck -y cleans up the orphaned inode.
    debugfs -w "$CACHE_EXT4" -R "unlink /nexus3-export" 2>&1 || true
    # Clean up orphaned inodes (including the unlinked dir tree) so the ext4
    # is in a consistent state before the write.
    e2fsck -y -f "$CACHE_EXT4" 2>&1 | tail -2 || true
    local tmp_blocker; tmp_blocker=$(mktemp)
    echo "NEXUS3_EXPORT_BLOCKER" > "$tmp_blocker"
    local dout
    if ! dout=$(debugfs -w "$CACHE_EXT4" -R "write $tmp_blocker /nexus3-export" 2>&1); then
        log "inject_nexus_export_blocker: debugfs write failed: ${dout}"
        rm -f "$tmp_blocker"
        return 1
    fi
    rm -f "$tmp_blocker"
    # Verify the blocker is a regular file (Type: regular), not a directory.
    # || true: grep exits 1 if no "Type:" match; without || true, set -e kills the script.
    local ftype
    ftype=$(debugfs -R "stat /nexus3-export" "$CACHE_EXT4" 2>&1 | grep "Type:") || true
    if echo "$ftype" | grep -q "regular"; then
        log "inject_nexus_export_blocker: injected — type: regular; MkdirAll will return ENOTDIR"
    else
        log "inject_nexus_export_blocker: WARN blocker not a regular file: ${ftype:-not found}"
        return 1
    fi
}

restore_nexus_export_blocker() {
    repair_ext4 || true
    # Use unlink (not rm) — handles both regular file and directory cases.
    debugfs -w "$CACHE_EXT4" -R "unlink /nexus3-export" 2>&1 || true
    # Clean up orphaned inodes left by unlink.
    e2fsck -y -f "$CACHE_EXT4" 2>&1 | tail -2 || true
    log "restore_nexus_export_blocker: /nexus3-export removed and ext4 repaired"
}

# ── Stage A: build-stderr manifest parser ────────────────────────────────────
# parse_manifest_stage_a <build_log>
# Parses "rootfs-size-manifest" lines emitted to build stderr by
# logRootfsSizeManifest (internal/core/agent/rootfs_manifest.go). The manifest
# is written synchronously after Solve() and before the integrity gates — making
# it non-racy and reachable even on builds that subsequently fail the gate.
# This is the AUTHORITATIVE Stage-A instrument (replaces the dead exec probe).
# Called in a subshell via $(...); must not update global counters.
parse_manifest_stage_a() {
    local blog="$1"
    if [[ ! -f "$blog" ]]; then
        echo "Stage_A_manifest:$(emit_fail "NO_LOG")"
        return
    fi
    # Entry lines have 3 spaces after the colon:
    #   "...rootfs-size-manifest:   <relpath padded to 60>  <size>"
    # The header line has 1 space ("rootfs-size-manifest: N file(s)...") and is
    # excluded by the 3-space filter.
    declare -A msizes
    while IFS= read -r mline; do
        local relpath msize
        relpath=$(echo "$mline" | awk '{print $(NF-1)}')
        msize=$(echo "$mline" | awk '{print $NF}')
        [[ -n "$relpath" ]] && [[ -n "$msize" ]] && msizes["$relpath"]="$msize"
    done < <(grep "rootfs-size-manifest:   " "$blog" 2>/dev/null || true)

    if [[ ${#msizes[@]} -eq 0 ]]; then
        echo "Stage_A_manifest:$(emit_fail "NO_MANIFEST_LINES")"
        return
    fi

    local tokens=()
    for f in "${TEST_FILES[@]}"; do
        local rp="testfiles/${f}"
        local msize="${msizes[$rp]:-}"
        local exp="${EXPECTED_SIZES[$f]:-}"
        if [[ -z "$msize" ]]; then
            tokens+=("${f}=$(emit_fail "manifest_absent,file=${f}")")
        elif [[ -z "$exp" ]]; then
            tokens+=("${f}=$(emit_fail "no_expected,file=${f},manifest_val=${msize}")")
        elif [[ "$msize" == "$exp" ]]; then
            tokens+=("${f}=OK")
        elif [[ "$msize" =~ ^[0-9]+$ ]]; then
            tokens+=("${f}=MANIFEST_FAIL(exp=${exp},got=${msize})")
        else
            tokens+=("${f}=$(emit_fail "manifest_err,file=${f},val=${msize}")")
        fi
    done

    # Check RUN-produced synthetic 40 MiB file (DEFECT-1 FIX target).
    local rp40m="${msizes[usr/local/bin/run-produced-40m]:-}"
    if [[ -z "$rp40m" ]]; then
        tokens+=("run-produced-40m=$(emit_fail "manifest_absent,file=run-produced-40m")")
    elif [[ "$rp40m" == "41943040" ]]; then
        tokens+=("run-produced-40m=OK(${rp40m}B)")
    elif [[ "$rp40m" == "33554432" ]]; then
        tokens+=("run-produced-40m=MANIFEST_FAIL(TRUNCATED_AT_32MiB,got=${rp40m})")
    else
        tokens+=("run-produced-40m=MANIFEST_FAIL(exp=41943040,got=${rp40m})")
    fi

    # Check docker-compose binary (apt-installed, faithful to original incident).
    local dc_msize="${msizes[usr/libexec/docker/cli-plugins/docker-compose]:-}"
    if [[ -z "$dc_msize" ]]; then
        tokens+=("docker-compose=$(emit_fail "manifest_absent,file=docker-compose")")
    elif [[ "$dc_msize" == "33554432" ]]; then
        tokens+=("docker-compose=MANIFEST_FAIL(TRUNCATED_AT_32MiB,got=${dc_msize})")
    else
        tokens+=("docker-compose=OK(${dc_msize}B)")
    fi

    # Check nexus3-agent binary (original fault seam).
    local agent_msize="${msizes[usr/sbin/nexus3-agent]:-}"
    local agent_exp; agent_exp=$(stat -c '%s' "$AGENT_BIN" 2>/dev/null || echo 0)
    # FIX 4(b): Guard agent_exp==0 (missing reference binary) the same way
    # stage_b_check_agent_size does at :290-296. Without this guard,
    # MANIFEST_FAIL(exp=0,got=N) would count as truncation evidence —
    # fabricated "TRUNCATION REPRODUCED" when the harness is misconfigured.
    if [[ "$agent_exp" -eq 0 ]]; then
        tokens+=("nexus3-agent=$(emit_fail "no_host_ref,AGENT_BIN=${AGENT_BIN}")")
    elif [[ -z "$agent_msize" ]]; then
        tokens+=("nexus3-agent=$(emit_fail "manifest_absent,file=nexus3-agent")")
    elif [[ "$agent_msize" == "$agent_exp" ]]; then
        tokens+=("nexus3-agent=OK(${agent_msize}B)")
    elif [[ "$agent_msize" == "33554432" ]]; then
        tokens+=("nexus3-agent=MANIFEST_FAIL(TRUNCATED_AT_32MiB,exp=${agent_exp})")
    else
        tokens+=("nexus3-agent=MANIFEST_FAIL(exp=${agent_exp},got=${agent_msize})")
    fi

    echo "Stage_A_manifest: ${tokens[*]}"
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

    log "=== ${label}: pre-build image list ==="
    local before; before=$(list_digests_sorted)
    # FIX 4(c): before-only-dead guard. If nexus3 image ls fails BEFORE the
    # build, comm -13 returns every AFTER digest as "new" — Stage B then
    # measures a pre-existing image and can report all-PASS. Detect the
    # sentinel and count as harness integrity failure.
    if [[ "$before" == "LIST_DIGESTS_FAILED" ]]; then
        local _blf_tok; _blf_tok=$(emit_fail "IMAGE_LIST_FAILED,stage=before,label=${label}")
        log "=== ${label}: ${_blf_tok} — cannot establish baseline; skipping iteration ==="
        (( TOTAL_FAIL++ )) || true
        ALL_RESULTS+=("${label} | Stage_A:list_failed | Stage_B:${_blf_tok} | elapsed=0s")
        return
    fi

    # DEFECT-2 FIX: write a per-iteration sentinel into testfiles/ so the COPY
    # layer's content hash changes each iteration, guaranteeing a content-
    # addressable cache miss for COPY even if buildkit uses file-content rather
    # than parent-snapshot-ID as the COPY cache key.
    local iter_uid; iter_uid="$(date +%s%N)-${label}"
    echo "${iter_uid}" > "${WORKSPACE}/testfiles/.repro-uid"

    # shellcheck disable=SC2086
    log "=== ${label}: starting build (timeout=${NEXUS3_BUILD_TASK_TIMEOUT}) build_flags='${extra_build_flags}' ==="
    local t0; t0=$(date +%s)
    # shellcheck disable=SC2086
    timeout "$BASH_BUILD_TIMEOUT" "$NEXUS3" create \
        "${REPRO_PROJECT}/${sandbox_name}" \
        --file "$ws" \
        --no-user-mounts \
        $extra_build_flags \
        > "$build_log" 2>&1 &
    local build_pid=$!
    log "=== ${label}: build pid=${build_pid} ==="

    # ── Wait for build + Stage A (build-stderr manifest) ─────────────────────
    # DEFECT-3 FIX: the live nexus3 exec probe is DELETED. Every historical run
    # shows "Stage A -- file_200m not seen (exec: )" with empty exec output;
    # nexus3 exec never worked against builder-VM sandboxes and produced zero
    # evidence across all sweeps. The probe is replaced with manifest parsing:
    # logRootfsSizeManifest (rootfs_manifest.go) writes file sizes to build
    # stderr immediately after Solve() and before the integrity gates -- non-racy
    # and reachable even on builds that subsequently fail the integrity gate.
    log "=== ${label}: waiting for build pid=${build_pid} ==="
    local build_exit=0
    wait "$build_pid" || build_exit=$?
    local t1; t1=$(date +%s)
    local elapsed=$(( t1 - t0 ))

    if [[ $build_exit -eq 124 ]]; then
        log "TIMEOUT: ${label} -- bash timeout ${BASH_BUILD_TIMEOUT}s exceeded (${elapsed}s)"
        ALL_RESULTS+=("${label} | Stage_A_manifest:TIMEOUT | Stage_B:TIMEOUT | elapsed=${elapsed}s")
        return
    fi

    # Parse manifest from build log (authoritative Stage A).
    local stage_a_line; stage_a_line=$(parse_manifest_stage_a "$build_log")
    log "=== ${label}: Stage A manifest (elapsed=${elapsed}s) -- ${stage_a_line} ==="

    # DEFECT-2 FIX: if elapsed < 45s on a successful build, warn that layer-cache
    # hits may have skipped the snapshotter materialization we need to exercise.
    # (apt-get + dd + COPY over 500 MiB should take >45s on a real cache miss.)
    if (( elapsed < 45 )) && [[ $build_exit -eq 0 ]]; then
        log "WARN: ${label} -- build completed in ${elapsed}s; potential layer-cache hit (apt+COPY expected >45s)"
        # Fix 7(a): CACHE_HIT_SUSPECTED is an impossible measurement — the snapshotter
        # materialization may not have run. Use emit_fail so it flows into TOTAL_FAIL
        # and cannot silently count as TOTAL_PASS.
        stage_a_line="${stage_a_line}|$(emit_fail "CACHE_HIT_SUSPECTED,${elapsed}s<45s")"
    fi

    # Count Stage A failures in the parent shell; track across both stages so a
    # Stage-A failure blocks TOTAL_PASS even when Stage-B is clean.
    # FAIL( matches MANIFEST_FAIL( and HARNESS_INTEGRITY_FAIL( (both contain FAIL().
    local iter_has_fail=0
    if echo "$stage_a_line" | grep -qF "FAIL("; then
        local a_fail_count
        a_fail_count=$(echo "$stage_a_line" | grep -oF "FAIL(" | wc -l)
        (( TOTAL_FAIL += a_fail_count )) || true
        iter_has_fail=1
    fi
    local _ta; _ta=$(count_trunc_evidence "$stage_a_line")
    (( TOTAL_TRUNC += _ta )) || true

    if [[ $build_exit -ne 0 ]]; then
        log "BUILD FAILED (exit=${build_exit}): ${label}"
        tail -20 "$build_log" | sed 's/^/  /' || true
        # Fix 6: BUILD_FAILED formerly had no FAIL( and no counter increment, so a
        # failed build was neither PASS nor FAIL and TOTAL_PASS+TOTAL_FAIL undercounted.
        local bf_tok; bf_tok=$(emit_fail "build_exit=${build_exit}")
        (( TOTAL_FAIL++ )) || true
        ALL_RESULTS+=("${label} | ${stage_a_line} | Stage_B:${bf_tok} | elapsed=${elapsed}s")
        local partial_sb; partial_sb=$(find_sandbox_by_name "$sandbox_name")
        cleanup_sandbox "$partial_sb"
        return
    fi

    # ── Stage B: packed ext4 measurement + hash verification ──────────────────
    log "=== ${label}: finding new image after build ==="
    local after; after=$(list_digests_sorted)
    local new_digest; new_digest=$(comm -13 <(echo "$before") <(echo "$after") | head -1) || new_digest=""

    if [[ -z "$new_digest" ]]; then
        log "WARN: ${label} — no new image digest (build-cache hit?)"
        stage_b_line="Stage_B:$(emit_fail "NO_NEW_IMAGE,cache_hit")"
    else
        local short="${new_digest#sha256:}"
        local img="${IMAGE_STORE}/${short}/artifact"
        if [[ ! -f "$img" ]]; then
            log "ERROR: ${label} — image file missing: ${img}"
            stage_b_line="Stage_B:$(emit_fail "IMAGE_FILE_MISSING")"
        else
            log "=== ${label}: Stage B — debugfs size check on ${short:0:16}... ==="
            local raw_b; raw_b=$(stage_b_measure "$img")
            # shellcheck disable=SC2086
            stage_b_line=$(format_result "Stage_B" $raw_b)
            log "$stage_b_line"

            # Hash verification for file_32m and file_elf.
            log "=== ${label}: Stage B — hash verification (file_32m, file_elf) ==="
            local hash_line; hash_line=$(stage_b_verify_hashes "$img") || iter_has_fail=1
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

            # DEFECT-1 FIX: RUN-produced file checks.
            log "=== ${label}: Stage B — run-produced-40m size check ==="
            local rp_check; rp_check=$(stage_b_check_run_produced_size "$img")
            log "  RunProduced: ${rp_check}"
            stage_b_line="${stage_b_line} | RunProduced:${rp_check}"

            log "=== ${label}: Stage B — docker-compose-v2 size check ==="
            local dc_check; dc_check=$(stage_b_check_dc_size "$img")
            log "  DockerCompose: ${dc_check}"
            stage_b_line="${stage_b_line} | DockerCompose:${dc_check}"

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

    # ── Per-iteration counters (Stage A manifest + Stage B) ───────────────────
    # Stage A failures were counted above; iter_has_fail carries that signal here.
    # Stage B FAIL( covers truncation FAILs and HARNESS_INTEGRITY_FAILs (both
    # contain FAIL(). TOTAL_PASS increments ONLY when the entire iteration is
    # clean — a Stage-A failure must not be masked by a clean Stage-B report.
    if echo "$stage_b_line" | grep -qF "FAIL("; then
        local fail_count
        fail_count=$(echo "$stage_b_line" | grep -oF "FAIL(" | wc -l)
        (( TOTAL_FAIL += fail_count )) || true
        iter_has_fail=1
    fi
    local _tb; _tb=$(count_trunc_evidence "$stage_b_line")
    (( TOTAL_TRUNC += _tb )) || true
    if [[ $iter_has_fail -eq 0 ]] && echo "$stage_b_line" | grep -qF "=PASS"; then
        (( TOTAL_PASS++ )) || true
    fi

    ALL_RESULTS+=("${label} | ${stage_a_line} | ${stage_b_line} | elapsed=${elapsed}s")

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

for req in "$NEXUS3" debugfs jq sha256sum strings resize2fs; do
    command -v "$req" > /dev/null 2>&1 || {
        echo "ERROR: required tool not found: ${req}"
        exit 1
    }
done

# ── Structural self-check: emit_fail is the sole HARNESS_INTEGRITY_FAIL( emitter ────
# Any literal HARNESS_INTEGRITY_FAIL( outside the emit_fail body is a regression.
# This gate converts the convention into a compile-time guarantee — it fires before
# any build so a future edit that bypasses emit_fail is caught immediately.
_hif_violations=$(grep -n 'HARNESS_INTEGRITY_FAIL(' "${BASH_SOURCE[0]}" \
    | grep -v 'echo "HARNESS_INTEGRITY_FAIL(' \
    | grep -v 'grep' \
    | grep -v '^[0-9]*:[[:space:]]*#' \
    | grep -v 'SELF-CHECK FAIL') || true
if [[ -n "$_hif_violations" ]]; then
    echo "SELF-CHECK FAIL: literal HARNESS_INTEGRITY_FAIL( found outside emit_fail body:" >&2
    echo "$_hif_violations" >&2
    exit 1
fi

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

# DEFECT-1 FIX: grow buildkit.ext4 to 20 GiB minimum before any builds.
# apt-get install layers (~30-80 MiB each) exhausted the original 10 GiB disk;
# the sparse ext4 is extended in-place at no real host-disk cost.
ensure_buildkit_disk_size 20

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
    # FIX 4(c): Phase 3 before-only-dead guard (same class as run_one_iteration).
    # If nexus3 image ls fails BEFORE the builds, comm -13 returns every
    # post-build digest as "new" — Stage B would measure a pre-existing image.
    p3_before_failed=0
    if [[ "$local_before" == "LIST_DIGESTS_FAILED" ]]; then
        p3_blf=$(emit_fail "IMAGE_LIST_FAILED,stage=before,phase=3")
        log "Phase 3: ${p3_blf} — cannot establish before-snapshot; Stage-B measurement will be skipped"
        (( TOTAL_FAIL++ )) || true
        ALL_RESULTS+=("pressure-phase3-before-list-failed | Stage_A:concurrent_not_captured | Stage_B:${p3_blf} | concurrent")
        p3_before_failed=1
    fi
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
        p3_build_ec=0
        wait "$pid" || p3_build_ec=$?
        if [[ $p3_build_ec -ne 0 ]]; then
            # Fix 3: a failed concurrent build was only a log line; it must be a
            # counted FAIL so the concurrency axis is observable.
            p3_wait_tok=$(emit_fail "build_exit=${p3_build_ec},pid=${pid}")
            log "Phase 3: concurrent build pid=${pid} exit=${p3_build_ec}: ${p3_wait_tok}"
            (( TOTAL_FAIL++ )) || true
            ALL_RESULTS+=("pressure-pid${pid} | Stage_A:concurrent_not_captured | Stage_B:${p3_wait_tok} | concurrent")
        fi
    done
    log "all concurrent builds finished"

    # Stage B for concurrent pressure images.
    local_after=$(list_digests_sorted)
    # FIX 4(c): if before-snapshot failed, comm would return all after-digests
    # as "new" (pre-existing images). Skip Stage B measurement entirely.
    if [[ $p3_before_failed -eq 1 ]]; then
        log "Phase 3: before-snapshot failed — Stage B measurement skipped to avoid false PASS on old images"
        new_digests=""
    else
    new_digests=$(comm -13 <(echo "$local_before") <(echo "$local_after") || true)
    fi
    p3_launch_count=${#pids[@]}
    p3_image_count=0
    [[ -n "$new_digests" ]] && p3_image_count=$(echo "$new_digests" | wc -l)

    # Fix 2: guard against empty/short new_digests — silence reads as "no truncation".
    # Emit one counted HARNESS_INTEGRITY_FAIL per build that produced no image.
    if [[ $p3_image_count -eq 0 ]]; then
        p3_tok=$(emit_fail "NO_NEW_IMAGE,phase=3,launches=${p3_launch_count}")
        log "Phase 3: no new images after ${p3_launch_count} concurrent builds — ${p3_tok}"
        (( TOTAL_FAIL++ )) || true
        ALL_RESULTS+=("pressure-phase3 | Stage_A:concurrent_not_captured | Stage_B:${p3_tok} | concurrent")
    elif [[ $p3_image_count -lt $p3_launch_count ]]; then
        p3_missing=$(( p3_launch_count - p3_image_count ))
        for (( _m = 1; _m <= p3_missing; _m++ )); do
            p3_tok=$(emit_fail "NO_NEW_IMAGE,phase=3,launches=${p3_launch_count},images=${p3_image_count}")
            log "Phase 3: missing image ${_m}/${p3_missing}: ${p3_tok}"
            (( TOTAL_FAIL++ )) || true
            ALL_RESULTS+=("pressure-phase3-missing${_m} | Stage_A:concurrent_not_captured | Stage_B:${p3_tok} | concurrent")
        done
    fi

    pidx=0
    for nd in $new_digests; do
        (( pidx++ )) || true
        short="${nd#sha256:}"
        img="${IMAGE_STORE}/${short}/artifact"
        if [[ ! -f "$img" ]]; then
            # Fix 1: image file absent was a WARN+continue with no FAIL(, no counter,
            # no ALL_RESULTS row — identical to the sequential path bug at :834.
            p3_img_tok=$(emit_fail "IMAGE_FILE_MISSING,digest=${short:0:16}")
            log "Phase 3: ${p3_img_tok}"
            (( TOTAL_FAIL++ )) || true
            ALL_RESULTS+=("pressure-img${pidx} | Stage_A:concurrent_not_captured | Stage_B:${p3_img_tok} | concurrent")
            continue
        fi
        plabel="pressure-img${pidx}"
        log "=== ${plabel}: Stage B — debugfs on ${short:0:16}... ==="
        raw_b=$(stage_b_measure "$img")
        # shellcheck disable=SC2086
        pb_line=$(format_result "Stage_B" $raw_b)
        log "$pb_line"
        p3_iter_has_fail=0
        hash_line=$(stage_b_verify_hashes "$img") || p3_iter_has_fail=1
        log "  HashVerify: ${hash_line}"
        ALL_RESULTS+=("${plabel} | Stage_A:concurrent_not_captured | ${pb_line} | HashVerify:${hash_line}")

        # Feed Phase 3 results into summary counters — same FAIL( discipline as
        # sequential phases. Loop runs in parent shell (after wait) so counter
        # updates propagate directly without file-based collection.
        if echo "$pb_line" | grep -qF "FAIL("; then
            p3_fail_count=$(echo "$pb_line" | grep -oF "FAIL(" | wc -l)
            (( TOTAL_FAIL += p3_fail_count )) || true
            p3_iter_has_fail=1
        fi
        if echo "$hash_line" | grep -qF "FAIL("; then
            p3_hfail_count=$(echo "$hash_line" | grep -oF "FAIL(" | wc -l)
            (( TOTAL_FAIL += p3_hfail_count )) || true
            p3_iter_has_fail=1
        fi
        # FIX 1: `local` is illegal outside a function. This loop is top-level
        # (inside `if [[ $PRESSURE -eq 1 ]]`). Under set -e, bash aborts with
        # "local: can only be used in a function" — Phase 3 never completes,
        # counters are zero, Phases 4/5/6 never run. bash -n does NOT catch this.
        _tp=$(count_trunc_evidence "$pb_line")
        _th=$(count_trunc_evidence "$hash_line")
        (( TOTAL_TRUNC += _tp + _th )) || true
        # Fix 4: Phase 3 never captures Stage_A — counting into TOTAL_PASS makes the
        # summary label "Stage-A+Stage-B, zero FAIL" false. Use a separate counter.
        if [[ $p3_iter_has_fail -eq 0 ]] && echo "$pb_line" | grep -qF "=PASS"; then
            (( TOTAL_PASS_P3++ )) || true
        fi
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

# ── Phase 5: export-scratch disk-pressure builds ─────────────────────────────
# CONDITION 1 from AC-2 sweep: fill the buildkit cache disk to within a target
# free space, then run builds that export >32 MiB files. Sweeps three regimes:
#   Level A: ~800 MiB free  — enough to start, might complete (some headroom)
#   Level B: ~400 MiB free  — borderline: export runs ~550 MiB rootfs → ENOSPC
#   Level C: ~100 MiB free  — very little: export fails early
#
# The key question: does buildkit's ExporterLocal propagate ENOSPC (Solve()
# returns an error) or silently truncate files (Solve() returns nil, files
# wrong size)? The Stage-A manifest localizes which.
#
# IMPORTANT: host free space is watched. If it drops below 15 GiB this phase
# aborts and restores. (Fill goes INTO the ext4 image, not to new host files,
# but the host file may grow if it was previously sparse.)
if [[ $DISK_PRESSURE -eq 1 ]]; then
    log "--- Phase 5: disk-pressure builds (CONDITION 1) ---"

    HOST_FREE_MIN_GIB=15
    check_host_free() {
        # FIX 2: use df -P (POSIX format) so NR==2 is always the data row even
        # when the device path wraps. Validate the output is numeric — an empty
        # avail_kb is silently coerced to 0, arithmetic (( 0 < 15 )) → abort,
        # DP_ABORTED=1, zero builds run, exit 0 "NO TRUNCATION OBSERVED".
        local avail_kb; avail_kb=$(df -P "$HOME" | awk 'NR==2{print $4}')
        if [[ ! "$avail_kb" =~ ^[0-9]+$ ]]; then
            local _tok; _tok=$(emit_fail "HOST_FREE_PROBE_DEAD,got=${avail_kb:0:30}")
            log "check_host_free: dead probe — ${_tok}"
            (( TOTAL_FAIL++ )) || true
            # Return 0 so caller's (( hfree < HOST_FREE_MIN_GIB )) fires and aborts
            # Phase 5 rather than proceeding with an unknown free-space value.
            echo 0
            return 0
        fi
        echo $(( avail_kb / 1024 / 1024 ))
    }

    PRESSURE_TMP_FILL="${SCRIPT_DIR}/logs/pressure_fill_$$.bin"
    DP_ABORTED=0

    for level_spec in "800:A" "400:B" "100:C"; do
        target_free_mb="${level_spec%%:*}"
        level_tag="${level_spec##*:}"

        hfree=$(check_host_free)
        if (( hfree < HOST_FREE_MIN_GIB )); then
            # FIX 2: DP_ABORTED=1 without a counted FAIL let Phase 5 exit 0
            # "NO TRUNCATION OBSERVED" while running zero builds. Emit a counted
            # FAIL so an aborted Phase 5 can never produce a clean verdict.
            local _abtok; _abtok=$(emit_fail "PHASE5_ABORT,host_free=${hfree}GiB,min=${HOST_FREE_MIN_GIB}GiB")
            log "Phase 5 ABORT: host free ${hfree} GiB < ${HOST_FREE_MIN_GIB} GiB threshold — ${_abtok}"
            (( TOTAL_FAIL++ )) || true
            DP_ABORTED=1
            break
        fi

        log "=== Phase 5 Level ${level_tag}: filling ext4 to ${target_free_mb} MiB free (host free: ${hfree} GiB) ==="
        _fill_rc=0
        fill_ext4_pressure "$target_free_mb" "$PRESSURE_TMP_FILL" || _fill_rc=$?
        if (( _fill_rc == 1 )); then
            # FIX 3: _fill_rc==1 means the disk is ALREADY at/below the target
            # free-space level — the pressure condition is already satisfied.
            # Previous code `continue`d here, skipping all builds. The more
            # pressured the disk, the fewer builds ran, and Phase 5 could report
            # "complete" with zero builds. Run the builds — already-pressured
            # is a better test condition, not a reason to skip.
            log "Phase 5 Level ${level_tag}: already at/below target — running builds at current pressure"
        elif (( _fill_rc != 0 )); then
            # TOTAL_FAIL already incremented inside fill_ext4_pressure.
            log "Phase 5 Level ${level_tag}: fill aborted due to error (see above)"
            continue
        fi

        # Run 2 builds at this pressure level (Level C = 1 build — fails early).
        iters=2; [[ "$level_tag" == "C" ]] && iters=1
        for (( pi = 1; pi <= iters; pi++ )); do
            hfree=$(check_host_free)
            if (( hfree < HOST_FREE_MIN_GIB )); then
                # FIX 2: same as outer abort — emit counted FAIL.
                local _abtok2; _abtok2=$(emit_fail "PHASE5_ABORT_MID,host_free=${hfree}GiB,min=${HOST_FREE_MIN_GIB}GiB")
                log "Phase 5 ABORT mid-level: host free ${hfree} GiB < ${HOST_FREE_MIN_GIB} GiB — ${_abtok2}"
                (( TOTAL_FAIL++ )) || true
                DP_ABORTED=1
                break
            fi
            run_one_iteration "dp-${level_tag}-$(printf '%02d' "$pi")" "$pi"
            log ""
        done
        [[ $DP_ABORTED -eq 1 ]] && break

        restore_ext4_pressure
        log ""
    done

    # Final restore (covers abort path where restore wasn't reached).
    restore_ext4_pressure

    if [[ $DP_ABORTED -eq 1 ]]; then
        log "Phase 5: ABORTED due to host free-space guard — partial results above"
    else
        log "Phase 5: complete"
    fi
    log ""
fi

# ── Phase 6: exportBase="" fallback branch ────────────────────────────────────
# CONDITION 2 from AC-2 sweep: force os.MkdirAll(exportScratchDir) to fail by
# pre-creating /nexus3-export as a regular FILE in the buildkit.ext4. The agent
# logs the warning and falls back to os.TempDir() = /tmp on the guest rootfs
# (a DIFFERENT filesystem from the cache disk). This branch is exercised for the
# first time here — every prior sweep took the happy path.
#
# After each build the blocker is re-injected because the guest agent may remove
# and recreate /nexus3-export. (The defer RemoveAll removes the dir; it cannot
# affect a pre-existing regular file, so re-injection is needed to be safe.)
if [[ $FALLBACK_BRANCH -eq 1 ]]; then
    log "--- Phase 6: exportBase=fallback branch (CONDITION 2) ---"
    log "Phase 6: injecting /nexus3-export regular-file blocker into buildkit.ext4"
    inject_nexus_export_blocker

    # Verify blocker is in place (|| true: grep exits 1 if "Type:" not found; with
    # set -e + pipefail that would kill the script without || true).
    blocker_check=$(debugfs -R "stat /nexus3-export" "$CACHE_EXT4" 2>&1 | grep "Type:" | head -1) || true
    log "Phase 6: blocker check: ${blocker_check:-not found}"

    FB_TRAP_SET=0
    cleanup_fallback() {
        log "Phase 6 cleanup trap: restoring nexus3-export blocker..."
        restore_nexus_export_blocker
    }
    trap cleanup_fallback EXIT

    for (( fbi = 1; fbi <= 2; fbi++ )); do
        log "=== Phase 6 build ${fbi}: fallback to /tmp (exportBase='') ==="
        # Re-inject before each build: the previous build's agent may have
        # removed /nexus3-export (via deferred RemoveAll on the *dir*, not the
        # file — but injecting fresh is cheap and safe).
        inject_nexus_export_blocker
        run_one_iteration "fb-$(printf '%02d' "$fbi")" "$fbi"
        log ""
    done

    restore_nexus_export_blocker
    trap - EXIT
    log "Phase 6: complete"
    log ""
fi

# ── Summary ───────────────────────────────────────────────────────────────────
log "=========================================="
log "SUMMARY"
log "  Build iterations fully intact (Stage-A+Stage-B, zero FAIL): ${TOTAL_PASS}"
log "  Phase 3 Stage-B-only passes (Stage-A not captured):        ${TOTAL_PASS_P3}"
log "  Total failures (truncation+hash+harness-integrity):        ${TOTAL_FAIL}"
log "  Truncation-specific failures (TOTAL_TRUNC):                ${TOTAL_TRUNC}"
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

if (( TOTAL_TRUNC > 0 )); then
    log "RESULT: TRUNCATION REPRODUCED — see FAIL entries above"
    exit 1
elif (( TOTAL_FAIL > 0 )); then
    log "RESULT: HARNESS INTEGRITY FAILURE (no truncation evidence) — see FAIL entries above"
    exit 1
else
    log "RESULT: NO TRUNCATION OBSERVED in this run"
    exit 0
fi
