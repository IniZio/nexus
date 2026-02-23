#!/bin/bash
set -e

NEXUS_DIR="/Users/newman/magic/nexus/packages/nexusd"
PLUGIN_SOURCE="/Users/newman/magic/nexus/packages/opencode/dist/index.js"

echo "=== Nexus Setup Script ==="

echo "Building nexus binaries..."
cd "$NEXUS_DIR"
go build -o nexus ./cmd/cli
go build -o nexusd ./cmd/daemon

echo "Creating symlinks..."
mkdir -p ~/.local/bin
ln -sf "$NEXUS_DIR/nexus" ~/.local/bin/nexus
ln -sf "$NEXUS_DIR/nexusd" ~/.local/bin/nexusd

echo "Installing OpenCode plugin..."
mkdir -p ~/.opencode/plugins
ln -sf "$PLUGIN_SOURCE" ~/.opencode/plugins/nexus.js

echo ""
echo "=== Setup Complete ==="
echo ""
echo "Add to PATH:"
echo '  export PATH="$HOME/.local/bin:$PATH"'
echo ""
echo "Verify:"
echo "  nexus --help"
echo "  ~/.opencode/plugins/nexus.js"
echo ""
echo "For project-level plugin:"
echo "  mkdir -p .opencode/plugins"
echo "  ln -sf $PLUGIN_SOURCE .opencode/plugins/nexus.js"
