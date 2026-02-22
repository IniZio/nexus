#!/bin/bash
# Example post-start lifecycle script
# This script runs after the workspace starts

echo "Starting post-start hooks for workspace..."

# Check if npm is available and install dependencies
if command -v npm &> /dev/null; then
    if [ -f "package.json" ]; then
        echo "Installing dependencies..."
        npm install
    fi
fi

# Check if there's a dev server to start
if [ -f "package.json" ]; then
    if grep -q '"dev"' package.json; then
        echo "Starting dev server in background..."
        npm run dev &
    fi
fi

echo "Post-start hooks complete"
