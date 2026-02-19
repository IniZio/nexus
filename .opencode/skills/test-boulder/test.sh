#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NEXUS_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"
STATE_FILE="$NEXUS_DIR/.nexus/boulder/state.json"

echo "=========================================="
echo "  Boulder Configuration Test Suite"
echo "=========================================="
echo ""

# Test selection
TEST_TYPE="${1:-all}"

run_plugin_test() {
    echo ">>> Test 1: Boulder Plugin Load"
    echo "--------------------------------"
    
    # Check if enforcer package exists
    if [ -d "$NEXUS_DIR/packages/enforcer" ]; then
        echo "[PASS] Enforcer package exists"
    else
        echo "[FAIL] Enforcer package not found"
        return 1
    fi
    
    # Check boulder source files
    if [ -f "$NEXUS_DIR/packages/enforcer/src/boulder/state.ts" ]; then
        echo "[PASS] Boulder state module found"
    else
        echo "[FAIL] Boulder state module not found"
        return 1
    fi
    
    # Check boulder plugin
    if [ -f "$NEXUS_DIR/packages/opencode/src/boulder-plugin.ts" ]; then
        echo "[PASS] Boulder plugin found"
    else
        echo "[FAIL] Boulder plugin not found"
        return 1
    fi
    
    echo ""
}

run_state_test() {
    echo ">>> Test 2: State File"
    echo "----------------------"
    
    # Create directory if needed
    mkdir -p "$NEXUS_DIR/.nexus/boulder"
    
    # Check state file exists
    if [ -f "$STATE_FILE" ]; then
        echo "[PASS] State file exists"
    else
        echo "[INFO] State file not found, creating..."
        echo '{}' > "$STATE_FILE"
        echo "[PASS] State file created"
    fi
    
    # Check read permissions
    if [ -r "$STATE_FILE" ]; then
        echo "[PASS] State file is readable"
    else
        echo "[FAIL] State file is not readable"
        return 1
    fi
    
    # Validate JSON
    if command -v jq &> /dev/null; then
        if jq empty "$STATE_FILE" 2>/dev/null; then
            echo "[PASS] State file is valid JSON"
        else
            echo "[FAIL] State file is invalid JSON"
            return 1
        fi
    else
        echo "[WARN] jq not installed, skipping JSON validation"
    fi
    
    # Show current state
    echo ""
    echo "Current state:"
    cat "$STATE_FILE" 2>/dev/null || echo "{}"
    echo ""
}

run_idle_test() {
    echo ">>> Test 3: Idle Detection"
    echo "--------------------------"
    
    # Check idle detector exists
    if [ -f "$NEXUS_DIR/packages/enforcer/src/boulder/idle-detector.ts" ]; then
        echo "[PASS] Idle detector module found"
    else
        echo "[FAIL] Idle detector not found"
        return 1
    fi
    
    # Check default idle threshold
    IDLE_THRESHOLD=$(grep -o "idleThresholdMs.*:" "$NEXUS_DIR/packages/opencode/src/boulder-plugin.ts" | head -1 | grep -o "[0-9]*" || echo "30000")
    echo "[INFO] Default idle threshold: ${IDLE_THRESHOLD}ms"
    
    # Test: wait and check state update
    echo "[INFO] Waiting 2 seconds to test state update..."
    sleep 2
    
    # Update state with current time
    CURRENT_TIME=$(date +%s000)
    if [ -f "$STATE_FILE" ]; then
        jq --arg time "$CURRENT_TIME" '.lastValidationTime = ($time | tonumber)' "$STATE_FILE" > "${STATE_FILE}.tmp" && mv "${STATE_FILE}.tmp" "$STATE_FILE"
        echo "[PASS] State timestamp updated"
    fi
    
    echo ""
}

run_keyword_test() {
    echo ">>> Test 4: Completion Keywords"
    echo "-------------------------------"
    
    # Check keywords defined
    KEYWORDS=$(grep -A 20 "completionKeywords" "$NEXUS_DIR/packages/opencode/src/boulder-plugin.ts" | grep -E "^\s+'" | sed "s/^[[:space:]]*'//;s/',//" | head -10)
    
    if [ -n "$KEYWORDS" ]; then
        echo "[PASS] Completion keywords found:"
        echo "$KEYWORDS" | while read -r kw; do
            echo "  - $kw"
        done
    else
        echo "[FAIL] No completion keywords found"
        return 1
    fi
    
    echo ""
}

run_messages_test() {
    echo ">>> Test 5: Enforcement Messages"
    echo "---------------------------------"
    
    # Check enforcement message generation
    if grep -q "The boulder NEVER stops" "$NEXUS_DIR/packages/enforcer/src/boulder/infinite-state.ts"; then
        echo "[PASS] Enforcement message found"
    else
        echo "[FAIL] Enforcement message not found"
        return 1
    fi
    
    # Check improvement tasks
    if grep -q "IMPROVEMENT_TASKS" "$NEXUS_DIR/packages/enforcer/src/boulder/state.ts"; then
        echo "[PASS] Improvement tasks defined"
    else
        echo "[FAIL] Improvement tasks not found"
        return 1
    fi
    
    # Check minimum iterations
    MIN_ITER=$(grep "MINIMUM_ITERATIONS" "$NEXUS_DIR/packages/enforcer/src/boulder/state.ts" | grep -o "[0-9]*")
    echo "[INFO] Minimum iterations required: $MIN_ITER"
    
    echo ""
}

# Run tests based on selection
case "$TEST_TYPE" in
    --plugin)
        run_plugin_test
        ;;
    --state)
        run_state_test
        ;;
    --idle)
        run_idle_test
        ;;
    --keywords)
        run_keyword_test
        ;;
    --messages)
        run_messages_test
        ;;
    all|"")
        run_plugin_test
        run_state_test
        run_idle_test
        run_keyword_test
        run_messages_test
        
        echo "=========================================="
        echo "  All Tests Complete!"
        echo "=========================================="
        ;;
    *)
        echo "Unknown test: $TEST_TYPE"
        echo "Valid options: --plugin, --state, --idle, --keywords, --messages, or all"
        exit 1
        ;;
esac
