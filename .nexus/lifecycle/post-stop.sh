#!/bin/bash
# Post-stop lifecycle script
# This script runs after the workspace stops

echo "Workspace stopped"

# Cleanup temporary files
rm -rf /tmp/nexus-* 2>/dev/null || true

echo "Post-stop complete"
