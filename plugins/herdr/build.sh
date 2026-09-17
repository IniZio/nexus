#!/bin/sh
set -eu
# NOTE: The live-herdr install / pane-open end-to-end test is DEFERRED and
# not run in CI (no herdr binary available on the build host).

PLUGIN_DIR="${HERDR_PLUGIN_ROOT:-$(cd "$(dirname "$0")" && pwd)}"
# GitHub repo that publishes releases (where `gh release upload` sends assets).
# NOTE: this is the GitHub OWNER (IniZio); the Go module path is also github.com/IniZio/nexus.
GITHUB_OWNER="IniZio"
GITHUB_REPO="nexus"
ASSET_NAME="nexus-linux-amd64"
DEFAULT_INSTALL_DIR="${HOME}/.local/bin"
INSTALL_DIR="${INSTALL_DIR:-$DEFAULT_INSTALL_DIR}"
VERSION_FILE="$PLUGIN_DIR/nexus-version"

# ── Shim target ───────────────────────────────────────────────────────────
# The shim is what herdr's hooks exec.  It lives in the plugin dir (the
# checkout) by default, so a proof run that points INSTALL_DIR at a temp dir
# would repoint the LIVE shim at a binary that is about to be deleted and
# break every hook (exit 127).  Such runs must also redirect the shim via
# NEXUS_SHIM_DIR; otherwise abort before touching anything.
SHIM_DIR="${NEXUS_SHIM_DIR:-$PLUGIN_DIR}"
if [ "$INSTALL_DIR" != "$DEFAULT_INSTALL_DIR" ] && [ -z "${NEXUS_SHIM_DIR:-}" ]; then
    echo "nexus: error: INSTALL_DIR=$INSTALL_DIR is not the default ($DEFAULT_INSTALL_DIR)," >&2
    echo "  but the shim would still be written to the checkout at $PLUGIN_DIR/nexus-shim.sh," >&2
    echo "  repointing the live plugin at a non-default binary.  Set NEXUS_SHIM_DIR=<dir>" >&2
    echo "  to write the shim elsewhere for this run." >&2
    exit 1
fi
mkdir -p "$SHIM_DIR"
SHIM="$SHIM_DIR/nexus-shim.sh"

# ── Portable SHA-256 checksum helper ─────────────────────────────────────
_sha256_check() {
    _asset="$1" _sums_file="$2"
    _expected="$(grep "$_asset" "$_sums_file" | awk '{print $1}')"
    if sha256sum --version 2>/dev/null | grep -q GNU; then
        _got="$(sha256sum "$_asset" | awk '{print $1}')"
    elif command -v shasum >/dev/null 2>&1; then
        _got="$(shasum -a 256 "$_asset" | awk '{print $1}')"
    else
        echo "nexus: error: no sha256 tool (need GNU sha256sum or shasum -a 256)" >&2
        return 1
    fi
    [ "$_got" = "$_expected" ]
}

# ── Platform guard ────────────────────────────────────────────────────────
OS="$(uname -s)"
ARCH="$(uname -m)"
if [ "$OS" != "Linux" ] || [ "$ARCH" != "x86_64" ]; then
    USE_LOCAL_CLIENT=0
    if [ -n "${NEXUS_LOCAL:-}" ]; then
        echo "nexus plugin: NEXUS_LOCAL set — skipping download, using NEXUS_CLIENT / PATH binary."
        USE_LOCAL_CLIENT=1
    elif [ ! -f "$VERSION_FILE" ]; then
        echo "nexus plugin: $VERSION_FILE absent — falling back to NEXUS_CLIENT / PATH binary." >&2
        USE_LOCAL_CLIENT=1
    fi

    if [ "$USE_LOCAL_CLIENT" = "1" ]; then
        CLIENT="${NEXUS_CLIENT:-$(command -v nexus-client 2>/dev/null || true)}"
        if [ -z "$CLIENT" ] || [ ! -x "$CLIENT" ]; then
            echo "nexus plugin: ${OS}/${ARCH} is a remote client and needs nexus-client on PATH (or NEXUS_CLIENT=<path>)." >&2
            echo "Run: herdr plugin install IniZio/nexus/plugins/herdr" >&2
            exit 1
        fi
    else
        VERSION="$(cat "$VERSION_FILE")"

        case "$OS" in
            Darwin) GOOS="darwin" ;;
            Linux)  GOOS="linux"  ;;
            *) echo "nexus plugin: ${OS}/${ARCH} is not a supported remote-client platform" >&2; exit 1 ;;
        esac
        case "$ARCH" in
            arm64|aarch64) GOARCH="arm64" ;;
            x86_64)        GOARCH="amd64" ;;
            *) echo "nexus plugin: ${ARCH} is not a supported remote-client architecture" >&2; exit 1 ;;
        esac
        CLIENT_ASSET="nexus-client-${GOOS}-${GOARCH}"
        CLIENT_BIN="$INSTALL_DIR/nexus-client"
        BASE_URL="${NEXUS_RELEASE_BASE_URL:-https://github.com/${GITHUB_OWNER}/${GITHUB_REPO}/releases/download}"

        SKIP_CLIENT_DOWNLOAD=0
        if [ -z "${NEXUS_FORCE_DOWNLOAD:-}" ] && [ -x "$CLIENT_BIN" ]; then
            EXISTING_VER="$("$CLIENT_BIN" version 2>/dev/null \
                | grep -oE '[0-9]+\.[0-9]+\.[0-9]+([-+][a-zA-Z0-9._]+)?' \
                | head -1)" || true
            if echo "${EXISTING_VER:-}" | grep -q -- '-dev'; then
                echo "nexus plugin: $CLIENT_BIN ($EXISTING_VER) is a dev build — keeping it (set NEXUS_FORCE_DOWNLOAD=1 to override)."
                SKIP_CLIENT_DOWNLOAD=1
            elif [ -n "$EXISTING_VER" ]; then
                HIGHEST="$(printf '%s\n%s\n' "$VERSION" "$EXISTING_VER" | sort -V | tail -1)"
                if [ "$HIGHEST" = "$EXISTING_VER" ] && [ "$EXISTING_VER" != "$VERSION" ]; then
                    echo "nexus plugin: $CLIENT_BIN ($EXISTING_VER) is newer than release $VERSION — keeping it (set NEXUS_FORCE_DOWNLOAD=1 to override)."
                    SKIP_CLIENT_DOWNLOAD=1
                fi
            fi
        fi

        if [ "$SKIP_CLIENT_DOWNLOAD" = "0" ]; then
            WORK_DIR="$(mktemp -d)"
            trap 'rm -rf "$WORK_DIR"' EXIT

            echo "nexus plugin: downloading ${CLIENT_ASSET} ${VERSION} …"
            curl --fail --location --silent --show-error \
                -o "$WORK_DIR/$CLIENT_ASSET" \
                "${BASE_URL}/${VERSION}/${CLIENT_ASSET}"
            curl --fail --location --silent --show-error \
                -o "$WORK_DIR/SHA256SUMS" \
                "${BASE_URL}/${VERSION}/SHA256SUMS"

            echo "nexus plugin: verifying checksum …"
            (cd "$WORK_DIR" && _sha256_check "$CLIENT_ASSET" SHA256SUMS) || {
                echo "nexus: error: checksum mismatch for ${CLIENT_ASSET}" >&2
                exit 1
            }

            mkdir -p "$INSTALL_DIR"
            install -m 0755 "$WORK_DIR/$CLIENT_ASSET" "$CLIENT_BIN"
            echo "nexus plugin: installed -> $CLIENT_BIN"
        fi
        CLIENT="$CLIENT_BIN"
    fi

    CLIENT="$(cd "$(dirname "$CLIENT")" && pwd)/$(basename "$CLIENT")"
    EXPECTED_ABI="$(cat "$PLUGIN_DIR/abi" 2>/dev/null)" || { echo "nexus: error: $PLUGIN_DIR/abi not found" >&2; exit 1; }
    GOT_ABI="$("$CLIENT" herdr abi 2>/dev/null)" || { echo "nexus: error: nexus-client herdr abi probe failed" >&2; exit 1; }
    if [ "$GOT_ABI" != "$EXPECTED_ABI" ]; then
        echo "nexus: error: ABI mismatch: plugin expects ${EXPECTED_ABI}, nexus-client reports ${GOT_ABI}" >&2
        echo "Reinstall: herdr plugin install ${GITHUB_OWNER}/${GITHUB_REPO}/plugins/herdr" >&2
        exit 1
    fi
    printf '#!/bin/sh\nexec "%s" "$@"\n' "$CLIENT" > "$SHIM"
    chmod +x "$SHIM"
    echo "nexus plugin: remote client — shim written -> $SHIM (nexus-client)"
    exit 0
fi

# ── Decide: download or fall back to PATH ─────────────────────────────────
USE_LOCAL=0
if [ -n "${NEXUS_LOCAL:-}" ]; then
    echo "nexus plugin: NEXUS_LOCAL set — skipping download, using PATH binary."
    USE_LOCAL=1
elif [ ! -f "$VERSION_FILE" ]; then
    echo "nexus plugin: $VERSION_FILE absent — falling back to PATH binary." >&2
    USE_LOCAL=1
fi

if [ "$USE_LOCAL" = "0" ]; then
    # ── Self-bootstrapping download path ──────────────────────────────────
    VERSION="$(cat "$VERSION_FILE")"
    BASE_URL="${NEXUS_RELEASE_BASE_URL:-https://github.com/${GITHUB_OWNER}/${GITHUB_REPO}/releases/download}"
    NEXUS="$INSTALL_DIR/nexus"

    # ── Version-check guard ───────────────────────────────────────────────
    SKIP_DOWNLOAD=0
    if [ -z "${NEXUS_FORCE_DOWNLOAD:-}" ] && [ -x "$NEXUS" ]; then
        _vc_exit=0
        "$NEXUS" herdr version-check --pin "$VERSION_FILE" >/dev/null 2>&1 || _vc_exit=$?
        _vc_ver="$("$NEXUS" version 2>/dev/null \
            | grep -oE '[0-9]+\.[0-9]+\.[0-9]+([-+][a-zA-Z0-9._]+)?' \
            | head -1)" || true
        case "$_vc_exit" in
            0)  echo "nexus plugin: installed ${_vc_ver} (matches pin)"; SKIP_DOWNLOAD=1 ;;
            11) echo "nexus plugin: kept ${_vc_ver} (newer than pin)"; SKIP_DOWNLOAD=1 ;;
            12) echo "nexus plugin: kept ${_vc_ver} (dev build — set NEXUS_FORCE_DOWNLOAD=1 to override)"; SKIP_DOWNLOAD=1 ;;
            10) ;; # pin-newer → download
            *)  echo "nexus plugin: warning: version-check returned unexpected exit code $_vc_exit — downloading" ;;
        esac
    fi

    if [ "$SKIP_DOWNLOAD" = "0" ]; then
        _pre_ver=""
        if [ -x "$NEXUS" ]; then
            _pre_ver="$("$NEXUS" version 2>/dev/null \
                | grep -oE '[0-9]+\.[0-9]+\.[0-9]+([-+][a-zA-Z0-9._]+)?' \
                | head -1)" || true
        fi

        WORK_DIR="$(mktemp -d)"
        trap 'rm -rf "$WORK_DIR"' EXIT

        echo "nexus plugin: downloading ${ASSET_NAME} ${VERSION} …"
        curl --fail --location --silent --show-error \
            -o "$WORK_DIR/$ASSET_NAME" \
            "${BASE_URL}/${VERSION}/${ASSET_NAME}"
        curl --fail --location --silent --show-error \
            -o "$WORK_DIR/SHA256SUMS" \
            "${BASE_URL}/${VERSION}/SHA256SUMS"

        echo "nexus plugin: verifying checksum …"
        (cd "$WORK_DIR" && _sha256_check "${ASSET_NAME}" SHA256SUMS) || {
            echo "nexus: error: checksum mismatch for ${ASSET_NAME}" >&2
            exit 1
        }

        mkdir -p "$INSTALL_DIR"
        install -m 0755 "$WORK_DIR/$ASSET_NAME" "$NEXUS"
        if [ -n "$_pre_ver" ]; then
            echo "nexus plugin: upgraded ${_pre_ver} -> ${VERSION#v}"
        else
            echo "nexus plugin: installed ${VERSION#v}"
        fi
    fi

    _ids_exit=0
    _ids_out="$("$NEXUS" herdr install-default-shell --write-config 2>&1)" || _ids_exit=$?
    if [ -n "$_ids_out" ]; then printf '%s\n' "$_ids_out"; fi
    if [ "$_ids_exit" != "0" ]; then
        echo "nexus plugin: warning: install-default-shell exited $_ids_exit (continuing)"
    fi

    _ki_exit=0
    _ki_ver="$("$NEXUS" version 2>/dev/null \
        | grep -oE '[0-9]+\.[0-9]+\.[0-9]+([-+][a-zA-Z0-9._]+)?' \
        | head -1)" || true
    if echo "${_ki_ver:-}" | grep -q -- '-dev'; then
        "$NEXUS" kernel install --version "${VERSION}" || _ki_exit=$?
    else
        "$NEXUS" kernel install || _ki_exit=$?
    fi
    if [ "$_ki_exit" != "0" ]; then
        echo "nexus plugin: warning: kernel install failed — run: nexus kernel install"
    fi
else
    # ── Local dev / PATH fallback ─────────────────────────────────────────
    NEXUS="$(command -v nexus 2>/dev/null)" || {
        echo "nexus: error: 'nexus' not found on PATH" >&2
        echo "Install nexus first: https://github.com/${GITHUB_OWNER}/${GITHUB_REPO}#install" >&2
        exit 1
    }
    NEXUS="$(readlink -f "$NEXUS")"

    # Smoke-test: herdr context-cwd must print the workspace_cwd we pass.
    GOT="$(HERDR_PLUGIN_CONTEXT_JSON='{"workspace_cwd":"/"}' "$NEXUS" herdr context-cwd 2>/dev/null)" || {
        echo "nexus: error: herdr context-cwd probe failed" >&2
        exit 1
    }
    if [ "$GOT" != "/" ]; then
        echo "nexus: error: herdr context-cwd returned '$GOT', expected '/'" >&2
        exit 1
    fi
fi

# ── ABI probe ─────────────────────────────────────────────────────────────
EXPECTED_ABI="$(cat "$PLUGIN_DIR/abi" 2>/dev/null)" || {
    echo "nexus: error: $PLUGIN_DIR/abi not found" >&2
    exit 1
}
GOT_ABI="$("$NEXUS" herdr abi 2>/dev/null)" || {
    echo "nexus: error: herdr abi probe failed" >&2
    exit 1
}
if [ "$GOT_ABI" != "$EXPECTED_ABI" ]; then
    echo "nexus: error: ABI mismatch: plugin expects ${EXPECTED_ABI}, binary reports ${GOT_ABI}" >&2
    echo "Reinstall: herdr plugin uninstall nexus && herdr plugin install ${GITHUB_OWNER}/${GITHUB_REPO}/plugins/herdr" >&2
    exit 1
fi

# ── herdr version guard ───────────────────────────────────────────────────
MIN_HERDR="0.9.0"
HERDR_VER="$(herdr --version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1)" || true
if [ -n "$HERDR_VER" ]; then
    LOWEST="$(printf '%s\n%s\n' "$MIN_HERDR" "$HERDR_VER" | sort -V | head -1)"
    if [ "$LOWEST" != "$MIN_HERDR" ]; then
        echo "nexus: error: herdr ${HERDR_VER} < ${MIN_HERDR}: upgrade herdr first (brew upgrade herdr / herdr update)" >&2
        exit 1
    fi
fi

# ── Write the shim (absolute path so herdr's minimal launchd PATH doesn't matter) ──
printf '#!/bin/sh\nexec "%s" "$@"\n' "$NEXUS" > "$SHIM"
chmod +x "$SHIM"

echo "nexus plugin: shim written -> $SHIM"
NEXUS_VER="$("$NEXUS" version 2>/dev/null | head -1)" || true
echo "nexus plugin: using $NEXUS ($NEXUS_VER)"

_doctor_out="$("$NEXUS" doctor 2>&1)" || true
if [ -n "$_doctor_out" ]; then printf '%s\n' "$_doctor_out"; fi
