#!/bin/bash
# Custom health check script
# Returns 0 if healthy, non-zero otherwise

TARGET_URL="${HEALTH_URL:-http://localhost:3000/health}"

if command -v curl &> /dev/null; then
    curl -sf "$TARGET_URL" > /dev/null 2>&1
    exit $?
elif command -v wget &> /dev/null; then
    wget -q "$TARGET_URL" > /dev/null 2>&1
    exit $?
else
    echo "No HTTP client available"
    exit 1
fi
