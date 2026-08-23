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
# Only Linux x86_64 has a released binary.  All other platforms must build
# from source.
OS="$(uname -s)"
ARCH="$(uname -m)"
if [ "$OS" != "Linux" ] || [ "$ARCH" != "x86_64" ]; then
    echo "nexus3 plugin: no released binary for ${OS}/${ARCH}." >&2
    echo "Build from source:  git clone https://github.com/${GITHUB_OWNER}/${GITHUB_REPO} && (cd ${GITHUB_REPO} && go build -o ~/.local/bin/nexus3 ./cmd/nexus3)" >&2
    echo "Then run:           nexus3 herdr install-default-shell" >&2
    exit 1
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

# ── Write the shim (absolute path so herdr's minimal launchd PATH doesn't matter) ──
SHIM="$PLUGIN_DIR/nexus3-shim.sh"
printf '#!/bin/sh\nexec "%s" "$@"\n' "$NEXUS3" > "$SHIM"
chmod +x "$SHIM"

echo "nexus3 plugin: shim written -> $SHIM"
