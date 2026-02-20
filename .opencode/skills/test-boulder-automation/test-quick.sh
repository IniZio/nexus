#!/bin/bash
# Quick Boulder Test - Local Mode
# Tests boulder without requiring OpenCode Server

set -e

PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../" && pwd)"
STATE_FILE="$PROJECT_DIR/.nexus/boulder/state.json"

echo "🪨 Boulder Quick Test"
echo "======================"
echo ""

# Check if state file exists
if [ ! -f "$STATE_FILE" ]; then
  echo "❌ State file not found: $STATE_FILE"
  exit 1
fi

# Function to read state
get_iteration() {
  cat "$STATE_FILE" | grep -o '"iteration":[0-9]*' | cut -d: -f2
}

get_last_enforcement() {
  cat "$STATE_FILE" | grep -o '"lastEnforcement":[0-9]*' | cut -d: -f2
}

# Test 1: Check state is valid
echo "Test 1: Validating state file..."
if ! cat "$STATE_FILE" | python3 -m json.tool > /dev/null 2>&1; then
  echo "❌ State file is not valid JSON"
  exit 1
fi
echo "✅ State file is valid JSON"

# Test 2: Check plugin exists
echo ""
echo "Test 2: Checking plugin exists..."
if [ ! -f "$PROJECT_DIR/.opencode/plugins/nexus-enforcer.js" ]; then
  echo "❌ Plugin not found"
  exit 1
fi
echo "✅ Plugin exists"

# Test 3: Check syntax
echo ""
echo "Test 3: Validating plugin syntax..."
if ! node --check "$PROJECT_DIR/.opencode/plugins/nexus-enforcer.js" 2>/dev/null; then
  echo "❌ Plugin has syntax errors"
  exit 1
fi
echo "✅ Plugin syntax is valid"

# Test 4: Show current status
echo ""
echo "Test 4: Current Boulder Status"
echo "--------------------------------"
echo "Iteration: $(get_iteration)"
echo "Last Enforcement: $(date -d @$(($(get_last_enforcement) / 1000)) '+%Y-%m-%d %H:%M:%S' 2>/dev/null || echo 'N/A')"
echo ""

# Test 5: Monitor for enforcement (quick mode)
echo "Test 5: Monitoring boulder..."
echo "Send a message in OpenCode and wait 30 seconds to see enforcement."
echo "Current time: $(date '+%H:%M:%S')"
echo ""

ITERATION_START=$(get_iteration)
echo "Starting iteration: $ITERATION_START"
echo "Waiting 35 seconds..."

for i in {35..1}; do
  printf "\r⏱️  %2d seconds remaining... " "$i"
  sleep 1
  
  # Check if enforcement happened
  CURRENT_ITERATION=$(get_iteration)
  if [ "$CURRENT_ITERATION" -gt "$ITERATION_START" ]; then
    printf "\r✅ Enforcement triggered! Iteration: $CURRENT_ITERATION          \n"
    break
  fi
done

if [ "$CURRENT_ITERATION" -eq "$ITERATION_START" ]; then
  printf "\r⚠️  No enforcement in 35 seconds                              \n"
  echo "This might be normal if cooldown is active."
fi

echo ""
echo "======================"
echo "Test Complete"
echo "======================"
echo ""
echo "Manual verification steps:"
echo "1. ✅ Check that toast notification appeared"
echo "2. ✅ Check that system reminder message appeared in chat"
echo "3. ✅ Check that iteration increased from $ITERATION_START to $(get_iteration)"
echo ""
echo "If all checks pass, the boulder is working correctly! 🪨"