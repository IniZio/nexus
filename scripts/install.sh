#!/bin/sh
# Env: NEXUS_VERSION (release tag, default latest), NEXUS_INSTALL_DIR (default ~/.local/bin)
set -eu

REPO="IniZio/nexus"
INSTALL_DIR="${NEXUS_INSTALL_DIR:-$HOME/.local/bin}"

die() {
    echo "nexus-install: $*" >&2
    exit 1
}

[ "$(uname -s)" = "Linux" ] || die "unsupported OS $(uname -s); nexus release binaries are linux-only"

case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) die "unsupported architecture $(uname -m)" ;;
esac

if command -v curl >/dev/null 2>&1; then
    fetch() { curl -fsSL -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
    fetch() { wget -q -O "$2" "$1"; }
else
    die "need curl or wget"
fi

if command -v sha256sum >/dev/null 2>&1; then
    sha256() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
    sha256() { shasum -a 256 "$1" | awk '{print $1}'; }
else
    die "need sha256sum or shasum"
fi

asset="nexus-linux-${arch}"
if [ -n "${NEXUS_VERSION:-}" ]; then
    base="https://github.com/${REPO}/releases/download/${NEXUS_VERSION}"
else
    base="https://github.com/${REPO}/releases/latest/download"
fi

mkdir -p "$INSTALL_DIR"
# tmp inside the install dir keeps the final mv an atomic same-fs rename
tmp="$(mktemp -d "${INSTALL_DIR}/.nexus-install.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "Downloading ${asset} (${NEXUS_VERSION:-latest})..."
fetch "${base}/${asset}" "$tmp/$asset" || die "download failed: ${base}/${asset}"
fetch "${base}/${asset}.sha256" "$tmp/$asset.sha256" || die "download failed: ${base}/${asset}.sha256"

want="$(awk '{print $1; exit}' "$tmp/$asset.sha256")"
got="$(sha256 "$tmp/$asset")"
[ -n "$want" ] || die "empty checksum file"
[ "$want" = "$got" ] || die "sha256 mismatch: expected $want, got $got"
echo "sha256 verified: $got"

chmod 0755 "$tmp/$asset"
mv -f "$tmp/$asset" "${INSTALL_DIR}/nexus.new"
mv -f "${INSTALL_DIR}/nexus.new" "${INSTALL_DIR}/nexus"
echo "Installed ${INSTALL_DIR}/nexus"

case ":${PATH}:" in
    *":${INSTALL_DIR}:"*) ;;
    *)
        echo
        echo "${INSTALL_DIR} is not on your PATH. Add it:"
        echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
        ;;
esac
