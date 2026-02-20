#!/bin/bash
# Boulder Test Automation Runner
# Uses OpenCode Server to test boulder enforcement end-to-end

set -e

# Configuration
SKILL_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SKILL_DIR/../../../" && pwd)"
CONFIG_FILE="$SKILL_DIR/config.json"
LOG_DIR="$SKILL_DIR/logs"
STATE_FILE="$PROJECT_DIR/.nexus/boulder/state.json"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Logging
mkdir -p "$LOG_DIR"
LOG_FILE="$LOG_DIR/test-$(date +%Y%m%d-%H%M%S).log"
SERVER_LOG="$LOG_DIR/server.log"

log() {
  echo -e "${GREEN}[TEST]${NC} $1" | tee -a "$LOG_FILE"
}

error() {
  echo -e "${RED}[ERROR]${NC} $1" | tee -a "$LOG_FILE"
}

warn() {
  echo -e "${YELLOW}[WARN]${NC} $1" | tee -a "$LOG_FILE"
}

# Load configuration
load_config() {
  if [ -f "$CONFIG_FILE" ]; then
    IDLE_THRESHOLD=$(cat "$CONFIG_FILE" | grep -o '"idleThresholdMs":[0-9]*' | cut -d: -f2)
    COOLDOWN=$(cat "$CONFIG_FILE" | grep -o '"cooldownMs":[0-9]*' | cut -d: -f2)
    TEST_TIMEOUT=$(cat "$CONFIG_FILE" | grep -o '"testTimeoutMs":[0-9]*' | cut -d: -f2)
  fi
  
  # Defaults
  IDLE_THRESHOLD=${IDLE_THRESHOLD:-30000}
  COOLDOWN=${COOLDOWN:-30000}
  TEST_TIMEOUT=${TEST_TIMEOUT:-120000}
}

# Reset boulder state
reset_state() {
  log "Resetting boulder state..."
  cat > "$STATE_FILE" << 'EOF'
{
  "iteration": 0,
  "lastActivity": 0,
  "lastEnforcement": 0,
  "failureCount": 0,
  "stopRequested": false,
  "status": "CONTINUOUS",
  "abortDetectedAt": null,
  "isRecovering": false,
  "sessionID": null,
  "enforcementTriggeredForThisIdlePeriod": false
}
EOF
  log "State reset complete"
}

# Check if OpenCode is installed
check_opencode() {
  if ! command -v opencode &> /dev/null; then
    error "OpenCode CLI not found. Please install it first."
    exit 1
  fi
  
  log "OpenCode version: $(opencode --version)"
}

# Start OpenCode server
start_server() {
  log "Starting OpenCode server..."
  
  # Check if server already running
  if curl -s http://localhost:8080/health &> /dev/null; then
    warn "Server already running on port 8080"
    return 0
  fi
  
  # Start server in background
  cd "$PROJECT_DIR"
  opencode server start --port 8080 > "$SERVER_LOG" 2>&1 &
  SERVER_PID=$!
  
  # Wait for server to be ready
  log "Waiting for server to start..."
  for i in {1..30}; do
    if curl -s http://localhost:8080/health &> /dev/null; then
      log "Server started successfully (PID: $SERVER_PID)"
      return 0
    fi
    sleep 1
  done
  
  error "Server failed to start within 30 seconds"
  kill $SERVER_PID 2>/dev/null || true
  exit 1
}

# Stop OpenCode server
stop_server() {
  log "Stopping OpenCode server..."
  
  # Try graceful shutdown
  if curl -s http://localhost:8080/health &> /dev/null; then
    opencode server stop --port 8080 2>/dev/null || true
  fi
  
  # Kill any remaining processes
  pkill -f "opencode server" 2>/dev/null || true
  
  log "Server stopped"
}

# Create test session via API
create_session() {
  log "Creating test session..."
  
  RESPONSE=$(curl -s -X POST http://localhost:8080/api/sessions \
    -H "Content-Type: application/json" \
    -d "{\"directory\": \"$PROJECT_DIR\", \"agent\": \"boulder-test\"}" 2>/dev/null)
  
  SESSION_ID=$(echo "$RESPONSE" | grep -o '"id":"[^"]*"' | cut -d'"' -f4)
  
  if [ -z "$SESSION_ID" ]; then
    error "Failed to create session"
    return 1
  fi
  
  log "Created session: $SESSION_ID"
  echo "$SESSION_ID"
}

# Send message to session
send_message() {
  local session_id=$1
  local message=$2
  
  curl -s -X POST "http://localhost:8080/api/sessions/$session_id/messages" \
    -H "Content-Type: application/json" \
    -d "{\"content\": \"$message\"}" > /dev/null 2>&1
}

# Get session messages
get_messages() {
  local session_id=$1
  
  curl -s "http://localhost:8080/api/sessions/$session_id/messages" 2>/dev/null
}

# Check if boulder enforcement appeared
check_enforcement() {
  local session_id=$1
  local iteration=$2
  
  MESSAGES=$(get_messages "$session_id")
  
  if echo "$MESSAGES" | grep -q "BOULDER ENFORCEMENT.*Iteration $iteration"; then
    return 0
  fi
  
  return 1
}

# Get current boulder iteration
get_iteration() {
  if [ -f "$STATE_FILE" ]; then
    cat "$STATE_FILE" | grep -o '"iteration":[0-9]*' | cut -d: -f2
  else
    echo "0"
  fi
}

# Test 1: Idle Detection
test_idle_detection() {
  log "=== Test 1: Idle Detection ==="
  
  reset_state
  
  SESSION_ID=$(create_session)
  if [ $? -ne 0 ]; then
    error "Failed to create session"
    return 1
  fi
  
  log "Sending initial message..."
  send_message "$SESSION_ID" "Starting boulder idle test"
  
  log "Waiting ${IDLE_THRESHOLD}ms (${IDLE_THRESHOLD}ms idle threshold + 5s buffer)..."
  sleep $((IDLE_THRESHOLD / 1000 + 5))
  
  log "Checking for enforcement..."
  
  ITERATION=$(get_iteration)
  if [ "$ITERATION" -lt 1 ]; then
    error "Boulder did not trigger (iteration: $ITERATION)"
    return 1
  fi
  
  if ! check_enforcement "$SESSION_ID" "$ITERATION"; then
    error "System message not found in conversation"
    return 1
  fi
  
  log "✅ Idle detection working (iteration: $ITERATION)"
  return 0
}

# Test 2: Cooldown
test_cooldown() {
  log "=== Test 2: Cooldown ==="
  
  SESSION_ID=$(create_session)
  
  # First enforcement
  log "Triggering first enforcement..."
  send_message "$SESSION_ID" "Test cooldown - first trigger"
  sleep $((IDLE_THRESHOLD / 1000 + 5))
  
  FIRST_ITERATION=$(get_iteration)
  log "First enforcement: iteration $FIRST_ITERATION"
  
  # Wait during cooldown (should NOT trigger)
  log "Waiting 20 seconds during cooldown..."
  sleep 20
  
  SECOND_ITERATION=$(get_iteration)
  if [ "$SECOND_ITERATION" -ne "$FIRST_ITERATION" ]; then
    error "Cooldown failed: iteration jumped from $FIRST_ITERATION to $SECOND_ITERATION"
    return 1
  fi
  
  log "✅ Cooldown working (no trigger during cooldown)"
  
  # Wait for cooldown to pass
  log "Waiting for cooldown to pass..."
  sleep $((COOLDOWN / 1000 - 20 + 5))
  
  THIRD_ITERATION=$(get_iteration)
  if [ "$THIRD_ITERATION" -le "$FIRST_ITERATION" ]; then
    error "Second enforcement did not trigger after cooldown"
    return 1
  fi
  
  log "✅ Second enforcement triggered after cooldown (iteration: $THIRD_ITERATION)"
  return 0
}

# Test 3: Activity Reset
test_activity_reset() {
  log "=== Test 3: Activity Reset ==="
  
  SESSION_ID=$(create_session)
  
  log "Starting idle period..."
  sleep 10
  
  log "Sending message to reset timer..."
  send_message "$SESSION_ID" "Resetting idle timer"
  
  log "Waiting only 15 seconds (should NOT trigger due to reset)..."
  sleep 15
  
  ITERATION=$(get_iteration)
  log "Iteration after partial wait: $ITERATION"
  
  log "Now waiting full 30 seconds..."
  sleep 20
  
  NEW_ITERATION=$(get_iteration)
  if [ "$NEW_ITERATION" -le "$ITERATION" ]; then
    error "Enforcement did not trigger after activity reset"
    return 1
  fi
  
  log "✅ Activity reset working"
  return 0
}

# Generate test report
generate_report() {
  local passed=$1
  local failed=$2
  
  cat > "$LOG_DIR/test-report.txt" << EOF
Boulder Test Report
===================
Date: $(date)
Duration: $(($(date +%s) - START_TIME)) seconds

Results:
- Passed: $passed
- Failed: $failed
- Total: $((passed + failed))

State File: $STATE_FILE
Logs: $LOG_FILE

$(if [ $failed -eq 0 ]; then
  echo "✅ ALL TESTS PASSED"
else
  echo "❌ SOME TESTS FAILED"
fi)
EOF

  cat "$LOG_DIR/test-report.txt"
}

# Main test runner
main() {
  log "🪨 Boulder Test Automation Starting..."
  START_TIME=$(date +%s)
  
  load_config
  check_opencode
  
  # Setup
  reset_state
  start_server
  
  # Run tests
  PASSED=0
  FAILED=0
  
  if test_idle_detection; then
    ((PASSED++))
  else
    ((FAILED++))
  fi
  
  if test_cooldown; then
    ((PASSED++))
  else
    ((FAILED++))
  fi
  
  if test_activity_reset; then
    ((PASSED++))
  else
    ((FAILED++))
  fi
  
  # Cleanup
  stop_server
  
  # Report
  generate_report $PASSED $FAILED
  
  if [ $FAILED -eq 0 ]; then
    log "✅ All tests passed!"
    exit 0
  else
    error "❌ $FAILED test(s) failed"
    exit 1
  fi
}

# Cleanup on exit
cleanup() {
  stop_server 2>/dev/null || true
}
trap cleanup EXIT

# Run main
main "$@"