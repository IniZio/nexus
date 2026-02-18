#!/bin/bash
set -e

NEXUS_VERSION="${NEXUS_VERSION:-latest}"
NEXUS_REPO="${NEXUS_REPO:-inizio/nexus}"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
BINARY_NAME="nexus"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

detect_os() {
    local os
    os=$(uname -s | tr '[:upper:]' '[:lower:]')
    case "$os" in
        linux*) echo "linux" ;;
        darwin*) echo "darwin" ;;
        msys*|cygwin*|mingw*) echo "windows" ;;
        *)
            log_error "Unsupported operating system: $os"
            exit 1
            ;;
    esac
}

detect_arch() {
    local arch
    arch=$(uname -m)
    case "$arch" in
        x86_64|amd64) echo "amd64" ;;
        aarch64|arm64) echo "arm64" ;;
        armv7l) echo "arm" ;;
        *)
            log_error "Unsupported architecture: $arch"
            exit 1
            ;;
    esac
}

get_binary_name() {
    local os="$1"
    local arch="$2"

    if [ "$os" = "windows" ]; then
        echo "${BINARY_NAME}-${os}-${arch}.exe"
    else
        echo "${BINARY_NAME}-${os}-${arch}"
    fi
}

get_download_url() {
    local os="$1"
    local arch="$2"
    local version="$3"
    local binary_name
    binary_name=$(get_binary_name "$os" "$arch")

    if [ "$version" = "latest" ]; then
        echo "https://github.com/${NEXUS_REPO}/releases/latest/download/${binary_name}"
    else
        echo "https://github.com/${NEXUS_REPO}/releases/download/v${version}/${binary_name}"
    fi
}

verify_checksum() {
    local file="$1"
    local os="$2"
    local arch="$3"

    local checksum_file
    checksum_file="nexus-${os}-${arch}.sha256"

    if [ "$NEXUS_REPO" = "inizio/nexus" ]; then
        local checksum_url
        if [ "$NEXUS_VERSION" = "latest" ]; then
            checksum_url="https://github.com/${NEXUS_REPO}/releases/latest/download/${checksum_file}"
        else
            checksum_url="https://github.com/${NEXUS_REPO}/releases/download/v${NEXUS_VERSION}/${checksum_file}"
        fi

        if curl -fsSL "$checksum_url" -o /tmp/"$checksum_file" 2>/dev/null; then
            local expected_checksum
            expected_checksum=$(cut -d' ' -f1 /tmp/"$checksum_file")
            local actual_checksum
            actual_checksum=$(sha256sum "$file" | cut -d' ' -f1)

            if [ "$expected_checksum" != "$actual_checksum" ]; then
                log_error "Checksum verification failed!"
                log_error "Expected: $expected_checksum"
                log_error "Actual: $actual_checksum"
                rm -f "$file"
                exit 1
            fi
            log_info "Checksum verified successfully"
            rm -f /tmp/"$checksum_file"
        else
            log_warn "Checksum file not found, skipping verification"
        fi
    fi
}

install_binary() {
    local os="$1"
    local arch="$2"
    local version="$3"

    local binary_name
    binary_name=$(get_binary_name "$os" "$arch")
    local download_url
    download_url=$(get_download_url "$os" "$arch" "$version")
    local temp_file
    temp_file=$(mktemp)

    log_info "Detected OS: $os, Architecture: $arch"
    log_info "Downloading $binary_name..."

    if ! curl -fsSL "$download_url" -o "$temp_file"; then
        log_error "Failed to download binary"
        log_error "URL: $download_url"
        rm -f "$temp_file"
        exit 1
    fi

    log_info "Download complete, verifying..."

    if [ -f "${temp_file}" ] && [ -s "${temp_file}" ]; then
        log_info "Binary downloaded successfully"
    else
        log_error "Downloaded file is empty or invalid"
        rm -f "$temp_file"
        exit 1
    fi

    verify_checksum "$temp_file" "$os" "$arch"

    if [ ! -d "$INSTALL_DIR" ]; then
        log_info "Creating install directory: $INSTALL_DIR"
        sudo mkdir -p "$INSTALL_DIR"
    fi

    local final_path="${INSTALL_DIR}/${BINARY_NAME}"
    if [ "$os" = "windows" ]; then
        final_path="${INSTALL_DIR}/${BINARY_NAME}.exe"
    fi

    log_info "Installing to $final_path..."

    if [ "$INSTALL_DIR" = "/usr/local/bin" ] || [ "$INSTALL_DIR" = "/usr/bin" ]; then
        sudo cp "$temp_file" "$final_path"
        sudo chmod +x "$final_path"
    else
        cp "$temp_file" "$final_path"
        chmod +x "$final_path"
    fi

    rm -f "$temp_file"

    if command -v "$BINARY_NAME" >/dev/null 2>&1; then
        log_info "Installation complete!"
        log_info "Run '$BINARY_NAME --help' to get started"
    else
        log_warn "Binary installed but not in PATH"
        log_info "Add '$INSTALL_DIR' to your PATH if needed"
    fi
}

main() {
    log_info "Nexus Installer v1.0.0"

    local os arch
    os=$(detect_os)
    arch=$(detect_arch)

    if [ "$NEXUS_VERSION" != "latest" ]; then
        log_info "Installing version: $NEXUS_VERSION"
    else
        log_info "Installing latest version"
    fi

    install_binary "$os" "$arch" "$NEXUS_VERSION"
}

main
