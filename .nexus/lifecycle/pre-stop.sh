#!/bin/bash
# Pre-stop lifecycle script
# This script runs before the workspace stops

echo "Running pre-stop cleanup..."

# Kill any background processes started by post-start
if [ -f "package.json" ]; then
    if grep -q '"dev"' package.json; then
        pkill -f "npm run dev" 2>/dev/null || true
    fi
fi

echo "Pre-stop complete"
