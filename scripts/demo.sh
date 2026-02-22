#!/bin/bash
# Nexus Workspace Demo

set -e

NEXUS="./packages/nexusd/nexus"

echo "Nexus Workspace Demo"
echo "===================="
echo ""

echo "Step 1: Check system status"
$NEXUS status
echo ""

echo "Step 2: List workspaces"
$NEXUS workspace list
echo ""

echo "Step 3: Create a new workspace"
$NEXUS workspace create demo-app --template node 2>/dev/null || echo "(workspace may already exist)"
echo ""

echo "Step 4: Set active workspace"
$NEXUS workspace use demo-app
echo ""

echo "Step 5: Execute command in workspace"
$NEXUS workspace exec demo-app -- echo "Hello from workspace!"
echo ""

echo "Step 6: Check sync status"
$NEXUS sync status demo-app 2>/dev/null || echo "(sync not configured)"
echo ""

echo "Step 7: Check boulder enforcement status"
$NEXUS boulder status 2>/dev/null || echo "(boulder not running)"
echo ""

echo "==================================="
echo "Demo complete!"
echo "==================================="
