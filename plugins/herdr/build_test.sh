#!/bin/sh
# Test: build.sh non-Linux branch downloads nexus3-client from NEXUS3_RELEASE_BASE_URL.
set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BUILD_SH="$SCRIPT_DIR/build.sh"
ABI="$(cat "$SCRIPT_DIR/abi")"

PASS=0
FAIL=0

ok()   { echo "PASS: $1"; PASS=$((PASS+1)); }
fail() { echo "FAIL: $1"; FAIL=$((FAIL+1)); }

make_fake_release() {
    _fr_dir="$1" _fr_ver="$2" _fr_os="$3" _fr_arch="$4"
    _fr_asset="nexus3-client-${_fr_os}-${_fr_arch}"
    mkdir -p "$_fr_dir"
    printf '#!/bin/sh\ncase "$*" in\n"herdr abi") echo "%s" ;;\n"version") echo "nexus3-client %s" ;;\n*) exit 1 ;;\nesac\n' \
        "$ABI" "$_fr_ver" > "$_fr_dir/$_fr_asset"
    chmod +x "$_fr_dir/$_fr_asset"
    (
        cd "$_fr_dir"
        if command -v sha256sum >/dev/null 2>&1; then
            sha256sum "$_fr_asset" > SHA256SUMS
        else
            shasum -a 256 "$_fr_asset" > SHA256SUMS
        fi
    )
}

make_uname_stub() {
    _us_dir="$1" _us_os="$2" _us_arch="$3"
    mkdir -p "$_us_dir"
    printf '#!/bin/sh\ncase "$1" in\n-s) echo "%s" ;;\n-m) echo "%s" ;;\n*) /usr/bin/uname "$@" ;;\nesac\n' \
        "$_us_os" "$_us_arch" > "$_us_dir/uname"
    chmod +x "$_us_dir/uname"
}

run_build() {
    _rb_os="$1" _rb_arch="$2" _rb_ver="$3" _rb_extra_env="$4"
    _rb_work="$(mktemp -d)"
    _rb_install="$_rb_work/install"
    _rb_shim="$_rb_work/shim"
    _rb_release="$_rb_work/release"
    _rb_uname="$_rb_work/bin"
    _rb_plugin="$_rb_work/plugin"
    mkdir -p "$_rb_install" "$_rb_shim" "$_rb_release" "$_rb_uname" "$_rb_plugin"

    cp "$SCRIPT_DIR/abi" "$_rb_plugin/abi"
    echo "$_rb_ver" > "$_rb_plugin/nexus3-version"

    _rb_goos="$(echo "$_rb_os" | tr '[:upper:]' '[:lower:]')"
    if [ "$_rb_arch" = "arm64" ] || [ "$_rb_arch" = "aarch64" ]; then
        _rb_goarch="arm64"
    else
        _rb_goarch="amd64"
    fi

    make_fake_release "$_rb_release" "$_rb_ver" "$_rb_goos" "$_rb_goarch"
    make_uname_stub "$_rb_uname" "$_rb_os" "$_rb_arch"

    _rb_result=0
    env \
        PATH="$_rb_uname:$PATH" \
        HERDR_PLUGIN_ROOT="$_rb_plugin" \
        INSTALL_DIR="$_rb_install" \
        NEXUS3_SHIM_DIR="$_rb_shim" \
        NEXUS3_RELEASE_BASE_URL="file://${_rb_release}" \
        NEXUS3_FORCE_DOWNLOAD=1 \
        $_rb_extra_env \
        sh "$BUILD_SH" >/dev/null 2>&1 || _rb_result=$?

    if [ "$_rb_result" != "0" ]; then
        echo "  build.sh exited $_rb_result" >&2
        rm -rf "$_rb_work"
        echo "fail:exit:$_rb_result"
        return
    fi

    _rb_ok="ok"
    [ -x "$_rb_install/nexus3-client" ] || { echo "  nexus3-client not installed" >&2; _rb_ok="fail:no-client"; }
    [ -x "$_rb_shim/nexus3-shim.sh" ]  || { echo "  shim not written" >&2; _rb_ok="fail:no-shim"; }

    rm -rf "$_rb_work"
    echo "$_rb_ok"
}

_r="$(run_build "Darwin" "arm64" "v0.1.1" "")"
[ "$_r" = "ok" ] && ok "Darwin/arm64 downloads, verifies, installs nexus3-client, writes shim" \
    || fail "Darwin/arm64 failed ($_r)"

_r="$(run_build "Darwin" "x86_64" "v0.1.1" "")"
[ "$_r" = "ok" ] && ok "Darwin/amd64 downloads, verifies, installs nexus3-client, writes shim" \
    || fail "Darwin/amd64 failed ($_r)"

_w3="$(mktemp -d)"
_c3="$_w3/nexus3-client"; _shim3="$_w3/shim"; _plug3="$_w3/plugin"; _uname3="$_w3/bin"
mkdir -p "$_shim3" "$_plug3"
cp "$SCRIPT_DIR/abi" "$_plug3/abi"
echo "v0.1.1" > "$_plug3/nexus3-version"
make_uname_stub "$_uname3" "Darwin" "arm64"
printf '#!/bin/sh\ncase "$*" in\n"herdr abi") echo "%s" ;;\n*) exit 0 ;;\nesac\n' "$ABI" > "$_c3"
chmod +x "$_c3"
_r3=0
env \
    PATH="$_uname3:$PATH" \
    HERDR_PLUGIN_ROOT="$_plug3" \
    INSTALL_DIR="$_w3/install" \
    NEXUS3_SHIM_DIR="$_shim3" \
    NEXUS3_LOCAL=1 \
    NEXUS3_CLIENT="$_c3" \
    sh "$BUILD_SH" >/dev/null 2>&1 || _r3=$?
if [ "$_r3" = "0" ] && [ -x "$_shim3/nexus3-shim.sh" ]; then
    ok "NEXUS3_LOCAL=1 uses NEXUS3_CLIENT, skips download"
else
    fail "NEXUS3_LOCAL=1 fallback failed (exit=$_r3)"
fi
rm -rf "$_w3"

_w4="$(mktemp -d)"
_plug4="$_w4/plugin"; _shim4="$_w4/shim"; _uname4="$_w4/bin"
mkdir -p "$_plug4" "$_shim4"
cp "$SCRIPT_DIR/abi" "$_plug4/abi"
_c4="$_w4/nexus3-client"
make_uname_stub "$_uname4" "Darwin" "arm64"
printf '#!/bin/sh\ncase "$*" in\n"herdr abi") echo "%s" ;;\n*) exit 0 ;;\nesac\n' "$ABI" > "$_c4"
chmod +x "$_c4"
_r4=0
env \
    PATH="$_uname4:$(dirname "$_c4"):$PATH" \
    HERDR_PLUGIN_ROOT="$_plug4" \
    INSTALL_DIR="$_w4/install" \
    NEXUS3_SHIM_DIR="$_shim4" \
    sh "$BUILD_SH" >/dev/null 2>&1 || _r4=$?
if [ "$_r4" = "0" ] && [ -x "$_shim4/nexus3-shim.sh" ]; then
    ok "Absent nexus3-version falls back to PATH nexus3-client"
else
    fail "Absent nexus3-version fallback failed (exit=$_r4)"
fi
rm -rf "$_w4"

_w5="$(mktemp -d)"
_plug5="$_w5/plugin"; _shim5="$_w5/shim"; _uname5="$_w5/bin"
mkdir -p "$_plug5" "$_shim5"
cp "$SCRIPT_DIR/abi" "$_plug5/abi"
echo "v0.1.1" > "$_plug5/nexus3-version"
make_uname_stub "$_uname5" "Darwin" "s390x"
_r5=0
env \
    PATH="$_uname5:$PATH" \
    HERDR_PLUGIN_ROOT="$_plug5" \
    INSTALL_DIR="$_w5/install" \
    NEXUS3_SHIM_DIR="$_shim5" \
    NEXUS3_RELEASE_BASE_URL="file:///nonexistent" \
    sh "$BUILD_SH" >/dev/null 2>&1 || _r5=$?
if [ "$_r5" = "1" ]; then
    ok "Unsupported triple (Darwin/s390x) exits 1"
else
    fail "Unsupported triple should exit 1, got $_r5"
fi
rm -rf "$_w5"


make_fake_nexus3() {
    _fn_path="$1"
    printf '%s\n' \
        '#!/bin/sh' \
        'printf "%s\n" "$*" >> "${FAKE_NEXUS3_LOG:-/dev/null}"' \
        'case "$*" in' \
        '    "--version") echo "nexus3 ${FAKE_NEXUS3_VER:-v0.1.1}" ;;' \
        '    "herdr abi") cat "$HERDR_PLUGIN_ROOT/abi" ;;' \
        '    "herdr version-check"*) exit "${FAKE_VC_EXIT:-0}" ;;' \
        '    "herdr install-default-shell"*) exit "${FAKE_IDS_EXIT:-0}" ;;' \
        '    "kernel install"*) exit "${FAKE_KI_EXIT:-0}" ;;' \
        '    "doctor") exit "${FAKE_DOCTOR_EXIT:-0}" ;;' \
        '    *) exit 0 ;;' \
        'esac' > "$_fn_path"
    chmod +x "$_fn_path"
}

make_fake_linux_release() {
    _flr_dir="$1"
    mkdir -p "$_flr_dir"
    make_fake_nexus3 "$_flr_dir/nexus3-linux-amd64"
    (cd "$_flr_dir" && sha256sum nexus3-linux-amd64 > SHA256SUMS)
}

run_linux_build() {
    _lvc="$1" _lver="$2" _lids="$3" _lki="$4" _ldoc="$5" _lextra="$6" _ldown="$7"
    _lwork="$(mktemp -d)"
    _lplug="$_lwork/plugin" _linst="$_lwork/install"
    _lshim="$_lwork/shim"  _lrel="$_lwork/release"
    _llog="$_lwork/nexus3.log"
    mkdir -p "$_lplug" "$_linst" "$_lshim" "$_lrel"
    cp "$SCRIPT_DIR/abi" "$_lplug/abi"
    echo "v0.2.0" > "$_lplug/nexus3-version"
    make_fake_nexus3 "$_linst/nexus3"
    [ "$_ldown" = "1" ] && make_fake_linux_release "$_lrel"
    _lexit=0
    env \
        HERDR_PLUGIN_ROOT="$_lplug" \
        INSTALL_DIR="$_linst" \
        NEXUS3_SHIM_DIR="$_lshim" \
        NEXUS3_RELEASE_BASE_URL="file://${_lrel}" \
        FAKE_NEXUS3_LOG="$_llog" \
        FAKE_NEXUS3_VER="$_lver" \
        FAKE_VC_EXIT="$_lvc" \
        FAKE_IDS_EXIT="$_lids" \
        FAKE_KI_EXIT="$_lki" \
        FAKE_DOCTOR_EXIT="$_ldoc" \
        $_lextra \
        sh "$BUILD_SH" >/dev/null 2>&1 || _lexit=$?
    printf '%s' "$_lwork:$_lexit:$_llog"
}

_lr="$(run_linux_build 0 "v0.2.0" 0 0 0 "" 0)"
_lw="${_lr%%:*}"; _lt="${_lr#*:}"; _le="${_lt%%:*}"; _ll="${_lt#*:}"
_l1ok=1
[ "$_le" = "0" ] || { echo "  exit=$_le" >&2; _l1ok=0; }
grep -q -- "--write-config" "$_ll" 2>/dev/null || { echo "  --write-config not in log" >&2; _l1ok=0; }
grep -q "^kernel install" "$_ll" 2>/dev/null || { echo "  kernel install not in log" >&2; _l1ok=0; }
grep -q "^doctor" "$_ll" 2>/dev/null || { echo "  doctor not in log" >&2; _l1ok=0; }
[ "$_l1ok" = "1" ] && ok "Linux: same (exit 0) skips download; --write-config, kernel install, doctor called" \
    || fail "Linux: version-check same failed"
rm -rf "$_lw"

_lr="$(run_linux_build 11 "v0.3.0" 0 0 0 "" 0)"
_lw="${_lr%%:*}"; _lt="${_lr#*:}"; _le="${_lt%%:*}"; _ll="${_lt#*:}"
_l2ok=1
[ "$_le" = "0" ] || { echo "  exit=$_le" >&2; _l2ok=0; }
grep -q -- "--write-config" "$_ll" 2>/dev/null || { echo "  --write-config not in log" >&2; _l2ok=0; }
grep -q "^kernel install" "$_ll" 2>/dev/null || { echo "  kernel install not in log" >&2; _l2ok=0; }
[ "$_l2ok" = "1" ] && ok "Linux: installed-newer (exit 11) keeps binary; --write-config, kernel install called" \
    || fail "Linux: version-check installed-newer failed"
rm -rf "$_lw"

_lr="$(run_linux_build 12 "v0.2.0-dev" 0 0 0 "" 0)"
_lw="${_lr%%:*}"; _lt="${_lr#*:}"; _le="${_lt%%:*}"; _ll="${_lt#*:}"
_l3ok=1
[ "$_le" = "0" ] || { echo "  exit=$_le" >&2; _l3ok=0; }
grep -q -- "--write-config" "$_ll" 2>/dev/null || { echo "  --write-config not in log" >&2; _l3ok=0; }
grep -q "^kernel install" "$_ll" 2>/dev/null || { echo "  kernel install not in log" >&2; _l3ok=0; }
[ "$_l3ok" = "1" ] && ok "Linux: dev build (exit 12) keeps binary; --write-config, kernel install called" \
    || fail "Linux: version-check dev build failed"
rm -rf "$_lw"

_lr="$(run_linux_build 10 "v0.2.0" 0 0 0 "" 1)"
_lw="${_lr%%:*}"; _lt="${_lr#*:}"; _le="${_lt%%:*}"; _ll="${_lt#*:}"
_l4ok=1
[ "$_le" = "0" ] || { echo "  exit=$_le" >&2; _l4ok=0; }
grep -q -- "--write-config" "$_ll" 2>/dev/null || { echo "  --write-config not in log" >&2; _l4ok=0; }
grep -q "^kernel install" "$_ll" 2>/dev/null || { echo "  kernel install not in log" >&2; _l4ok=0; }
grep -q "^doctor" "$_ll" 2>/dev/null || { echo "  doctor not in log" >&2; _l4ok=0; }
[ "$_l4ok" = "1" ] \
    && ok "Linux: pin-newer downloads; --write-config, kernel install, doctor called" \
    || fail "Linux: pin-newer download test failed"
rm -rf "$_lw"

_lr="$(run_linux_build 0 "v0.2.0" 0 0 0 "NEXUS3_FORCE_DOWNLOAD=1" 1)"
_lw="${_lr%%:*}"; _lt="${_lr#*:}"; _le="${_lt%%:*}"; _ll="${_lt#*:}"
if [ "$_le" = "0" ] && grep -q "^kernel install" "$_ll" 2>/dev/null; then
    ok "Linux: NEXUS3_FORCE_DOWNLOAD=1 always downloads"
else
    fail "Linux: NEXUS3_FORCE_DOWNLOAD=1 test failed (exit=$_le)"
fi
rm -rf "$_lw"

_lr="$(run_linux_build 0 "v0.2.0" 0 0 1 "" 0)"
_lw="${_lr%%:*}"; _lt="${_lr#*:}"; _le="${_lt%%:*}"
if [ "$_le" = "0" ]; then
    ok "Linux: doctor failure (exit 1) is non-fatal"
else
    fail "Linux: doctor failure should not fail install (exit=$_le)"
fi
rm -rf "$_lw"

_lr="$(run_linux_build 10 "v0.2.0-dev" 0 0 0 "" 1)"
_lw="${_lr%%:*}"; _lt="${_lr#*:}"; _le="${_lt%%:*}"; _ll="${_lt#*:}"
if [ "$_le" = "0" ] && grep -q "^kernel install --version" "$_ll" 2>/dev/null; then
    ok "Linux: kernel install passes --version for dev binary"
else
    fail "Linux: kernel install --version for dev binary failed (exit=$_le)"
fi
rm -rf "$_lw"

echo ""
echo "Results: $PASS passed, $FAIL failed"
[ "$FAIL" = "0" ] && exit 0 || exit 1
