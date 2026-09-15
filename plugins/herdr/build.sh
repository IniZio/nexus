#!/bin/sh
set -eu
# NOTE: The live-herdr install / pane-open end-to-end test is DEFERRED and
# not run in CI (no herdr binary available on the build host).

PLUGIN_DIR="${HERDR_PLUGIN_ROOT:-$(cd "$(dirname "$0")" && pwd)}"
# GitHub repo that publishes releases (where `gh release upload` sends assets).
# NOTE: this is the GitHub OWNER (IniZio); the Go module path is also github.com/IniZio/nexus3.
GITHUB_OWNER="IniZio"
GITHUB_REPO="nexus3"
ASSET_NAME="nexus3-linux-amd64"
INSTALL_DIR="${HOME}/.local/bin"

# ── Platform guard ────────────────────────────────────────────────────────
# Linux/x86_64 gets the full nexus3 binary (host side: VMs, panes, verbs).
# Every other platform is a REMOTE CLIENT: the only thing that runs there is
# the [[startup]] hook that mirrors a nexus3 host's auto port-forwards, and
# that ships as the small nexus3-client binary because the full CLI does not
# build off Linux. The shim points at whichever binary this platform uses.
OS="$(uname -s)"
ARCH="$(uname -m)"
if [ "$OS" != "Linux" ] || [ "$ARCH" != "x86_64" ]; then
    CLIENT="${NEXUS3_CLIENT:-$(command -v nexus3-client 2>/dev/null || true)}"
    if [ -z "$CLIENT" ] || [ ! -x "$CLIENT" ]; then
        echo "nexus3 plugin: ${OS}/${ARCH} is a remote client and needs nexus3-client on PATH (or NEXUS3_CLIENT=<path>)." >&2
        echo "Build it on any machine with Go:  GOOS=$(echo "$OS" | tr '[:upper:]' '[:lower:]') GOARCH=<arch> go build -o nexus3-client ./cmd/nexus3-client" >&2
        exit 1
    fi
    CLIENT="$(cd "$(dirname "$CLIENT")" && pwd)/$(basename "$CLIENT")"
    EXPECTED_ABI="$(cat "$PLUGIN_DIR/abi" 2>/dev/null)" || { echo "nexus3: error: $PLUGIN_DIR/abi not found" >&2; exit 1; }
    GOT_ABI="$("$CLIENT" herdr abi 2>/dev/null)" || { echo "nexus3: error: nexus3-client herdr abi probe failed" >&2; exit 1; }
    if [ "$GOT_ABI" != "$EXPECTED_ABI" ]; then
        echo "nexus3: error: ABI mismatch: plugin expects ${EXPECTED_ABI}, nexus3-client reports ${GOT_ABI}" >&2
        exit 1
    fi
    SHIM="$PLUGIN_DIR/nexus3-shim.sh"
    printf '#!/bin/sh\nexec "%s" "$@"\n' "$CLIENT" > "$SHIM"
    chmod +x "$SHIM"
    echo "nexus3 plugin: remote client — shim written -> $SHIM (nexus3-client)"
    exit 0
fi

# ── Decide: download or fall back to PATH ─────────────────────────────────
# Local dev: set NEXUS3_LOCAL=1, or omit plugins/herdr/nexus3-version.
VERSION_FILE="$PLUGIN_DIR/nexus3-version"
USE_LOCAL=0
if [ -n "${NEXUS3_LOCAL:-}" ]; then
    echo "nexus3 plugin: NEXUS3_LOCAL set — skipping download, using PATH binary."
    USE_LOCAL=1
elif [ ! -f "$VERSION_FILE" ]; then
    echo "nexus3 plugin: $VERSION_FILE absent — falling back to PATH binary." >&2
    USE_LOCAL=1
fi

if [ "$USE_LOCAL" = "0" ]; then
    # ── Self-bootstrapping download path ──────────────────────────────────
    VERSION="$(cat "$VERSION_FILE")"
    BASE_URL="https://github.com/${GITHUB_OWNER}/${GITHUB_REPO}/releases/download/${VERSION}"

    WORK_DIR="$(mktemp -d)"
    trap 'rm -rf "$WORK_DIR"' EXIT

    echo "nexus3 plugin: downloading ${ASSET_NAME} ${VERSION} …"
    curl --fail --location --silent --show-error \
        -o "$WORK_DIR/$ASSET_NAME" \
        "${BASE_URL}/${ASSET_NAME}"
    curl --fail --location --silent --show-error \
        -o "$WORK_DIR/SHA256SUMS" \
        "${BASE_URL}/SHA256SUMS"

    echo "nexus3 plugin: verifying checksum …"
    # sha256sum -c reads the filename from the SUMS file; cd so relative paths match.
    (cd "$WORK_DIR" && grep "${ASSET_NAME}" SHA256SUMS | sha256sum --check --status) || {
        echo "nexus3: error: checksum mismatch for ${ASSET_NAME}" >&2
        exit 1
    }

    mkdir -p "$INSTALL_DIR"
    install -m 0755 "$WORK_DIR/$ASSET_NAME" "$INSTALL_DIR/nexus3"
    NEXUS3="$INSTALL_DIR/nexus3"
    echo "nexus3 plugin: installed -> $NEXUS3"

    # Hard-link guest shell + print config.toml default_shell snippet.
    "$NEXUS3" herdr install-default-shell || {
        echo "nexus3: error: install-default-shell failed" >&2
        exit 1
    }
else
    # ── Local dev / PATH fallback ─────────────────────────────────────────
    NEXUS3="$(command -v nexus3 2>/dev/null)" || {
        echo "nexus3: error: 'nexus3' not found on PATH" >&2
        echo "Install nexus3 first: https://github.com/${GITHUB_OWNER}/${GITHUB_REPO}#install" >&2
        exit 1
    }
    NEXUS3="$(readlink -f "$NEXUS3")"

    # Smoke-test: herdr context-cwd must print the workspace_cwd we pass.
    GOT="$(HERDR_PLUGIN_CONTEXT_JSON='{"workspace_cwd":"/"}' "$NEXUS3" herdr context-cwd 2>/dev/null)" || {
        echo "nexus3: error: herdr context-cwd probe failed" >&2
        exit 1
    }
    if [ "$GOT" != "/" ]; then
        echo "nexus3: error: herdr context-cwd returned '$GOT', expected '/'" >&2
        exit 1
    fi
fi

# ── ABI probe ─────────────────────────────────────────────────────────────
EXPECTED_ABI="$(cat "$PLUGIN_DIR/abi" 2>/dev/null)" || {
    echo "nexus3: error: $PLUGIN_DIR/abi not found" >&2
    exit 1
}
GOT_ABI="$("$NEXUS3" herdr abi 2>/dev/null)" || {
    echo "nexus3: error: herdr abi probe failed" >&2
    exit 1
}
if [ "$GOT_ABI" != "$EXPECTED_ABI" ]; then
    echo "nexus3: error: ABI mismatch: plugin expects ${EXPECTED_ABI}, binary reports ${GOT_ABI}" >&2
    echo "Reinstall: herdr plugin uninstall nexus3 && herdr plugin install ${GITHUB_OWNER}/${GITHUB_REPO}/plugins/herdr" >&2
    exit 1
fi

# ── herdr version guard ───────────────────────────────────────────────────
# min_herdr_version = "0.9.0" in the manifest prevents initial install on
# herdr < 0.9 but does NOT block a stale binary. Guard explicitly because
# `herdr machine list --json` (required by local-agent-startup) did not
# exist before 0.9.
MIN_HERDR="0.9.0"
HERDR_VER="$(herdr --version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1)" || true
if [ -n "$HERDR_VER" ]; then
    # Compare using sort -V (version sort) — POSIX sort lacks -V, but sort from GNU coreutils on Linux has it.
    LOWEST="$(printf '%s\n%s\n' "$MIN_HERDR" "$HERDR_VER" | sort -V | head -1)"
    if [ "$LOWEST" != "$MIN_HERDR" ]; then
        echo "nexus3: error: herdr ${HERDR_VER} < ${MIN_HERDR}: upgrade herdr first (brew upgrade herdr / herdr update)" >&2
        exit 1
    fi
fi

# ── Write the shim (absolute path so herdr's minimal launchd PATH doesn't matter) ──
SHIM="$PLUGIN_DIR/nexus3-shim.sh"
printf '#!/bin/sh\nexec "%s" "$@"\n' "$NEXUS3" > "$SHIM"
chmod +x "$SHIM"

echo "nexus3 plugin: shim written -> $SHIM"
