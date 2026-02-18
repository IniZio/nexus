#!/bin/bash
# Nexus Infinite Dogfooding Daemon
# Truly non-stop - auto-restarts, runs in background, never gives up

NEXUS_DIR="/home/newman/magic/nexus-dev/nexus"
LOG_DIR="$NEXUS_DIR/.nexus/dogfooding/logs"
PID_FILE="$LOG_DIR/dogfooding.pid"
FRICTION_LOG="$NEXUS_DIR/.nexus/dogfooding/friction-log.md"
ITERATION_FILE="$LOG_DIR/iteration.count"

echo "🔥 Nexus Infinite Dogfooding Daemon"
echo "==================================="
echo "Starting at: $(date)"
echo "PID: $$"
echo ""

# Ensure directories exist
mkdir -p "$LOG_DIR"

# Save PID
echo $$ > "$PID_FILE"

# Initialize iteration counter if not exists
if [ ! -f "$ITERATION_FILE" ]; then
    echo "0" > "$ITERATION_FILE"
fi

# Function to log with timestamp
log() {
    local level="$1"
    local message="$2"
    local timestamp=$(date '+%Y-%m-%d %H:%M:%S')
    echo "[$timestamp] [$level] $message" | tee -a "$LOG_DIR/daemon.log"
}

# Function to increment iteration counter
increment_iteration() {
    local count=$(cat "$ITERATION_FILE")
    echo $((count + 1)) > "$ITERATION_FILE"
    echo "$((count + 1))"
}

# Function to get current iteration
get_iteration() {
    cat "$ITERATION_FILE"
}

# Function to log friction
log_friction() {
    local severity="$1"
    local description="$2"
    local timestamp=$(date '+%Y-%m-%d %H:%M:%S')
    
    cat >> "$FRICTION_LOG" << EOF

### $timestamp - $severity (Auto-Logged by Daemon)
$description

EOF
    log "FRICTION" "$severity: $description"
}

# Function to analyze friction and generate task
generate_task_from_friction() {
    if [ ! -f "$FRICTION_LOG" ]; then
        echo "initial-setup"
        return
    fi
    
    # Count recent friction points (last 20 lines)
    local recent_friction=$(tail -20 "$FRICTION_LOG" 2>/dev/null)
    
    if echo "$recent_friction" | grep -qi "docker.*compose"; then
        echo "fix-docker-compose"
    elif echo "$recent_friction" | grep -qi "null\|sql"; then
        echo "fix-database-nulls"
    elif echo "$recent_friction" | grep -qi "slow\|performance"; then
        echo "optimize-performance"
    elif echo "$recent_friction" | grep -qi "build.*fail\|compile"; then
        echo "fix-build-errors"
    elif echo "$recent_friction" | grep -qi "test.*fail"; then
        echo "fix-test-failures"
    else
        # Rotate through improvement tasks
        local iteration=$(get_iteration)
        local task_num=$((iteration % 5))
        case $task_num in
            0) echo "improve-documentation" ;;
            1) echo "add-cli-commands" ;;
            2) echo "optimize-telemetry" ;;
            3) echo "enhance-templates" ;;
            4) echo "fix-edge-cases" ;;
        esac
    fi
}

# Function to run a full iteration
run_iteration() {
    local iteration=$(increment_iteration)
    
    log "INFO" "═══════════════════════════════════════════════════════"
    log "INFO" "🔄 Iteration $iteration - $(date)"
    log "INFO" "═══════════════════════════════════════════════════════"
    
    cd "$NEXUS_DIR" || {
        log "ERROR" "Failed to cd to $NEXUS_DIR"
        return 1
    }
    
    # Check telemetry
    log "INFO" "Checking telemetry..."
    ./nexus stats 2>&1 | head -20 >> "$LOG_DIR/stats.log" || {
        log_friction "LOW" "Stats command failed in iteration $iteration"
    }
    
    # Get insights
    log "INFO" "Getting insights..."
    ./nexus insights 2>&1 | head -10 >> "$LOG_DIR/insights.log" || {
        log_friction "LOW" "Insights command failed in iteration $iteration"
    }
    
    # Analyze friction and generate task
    local task=$(generate_task_from_friction)
    log "INFO" "Auto-generated task: $task"
    
    # Create workspace for this task
    local iteration_id=$(date '+%Y%m%d-%H%M%S')
    local workspace_name="dogfood-${task}-${iteration_id}"
    
    log "INFO" "Creating workspace: $workspace_name"
    ./nexus workspace create "$workspace_name" --template go-postgres 2>&1 | tee -a "$LOG_DIR/workspace.log" || {
        log_friction "MEDIUM" "Workspace creation failed for $workspace_name in iteration $iteration"
    }
    
    # Run tests
    log "INFO" "Running tests..."
    if go test ./pkg/coordination/... -v 2>&1 | tee -a "$LOG_DIR/test.log" | tail -5; then
        log "INFO" "Tests passed"
    else
        log_friction "HIGH" "Tests failed in iteration $iteration"
    fi
    
    # Build
    log "INFO" "Building..."
    if go build -o nexus ./cmd/nexus/ 2>&1 | tee -a "$LOG_DIR/build.log"; then
        log "INFO" "Build successful"
    else
        log_friction "CRITICAL" "Build failed in iteration $iteration"
        return 1
    fi
    
    # Auto-commit if there are changes
    if ! git diff --quiet HEAD 2>/dev/null; then
        log "INFO" "Auto-committing changes..."
        git add -A 2>/dev/null || true
        git commit -m "dogfood(auto): iteration $iteration - $task improvements" 2>/dev/null || true
        log "INFO" "Auto-committed iteration $iteration"
    fi
    
    log "INFO" "Iteration $iteration complete"
    return 0
}

# Main loop with auto-restart
main_loop() {
    local consecutive_failures=0
    local max_failures=5
    
    while true; do
        if run_iteration; then
            consecutive_failures=0
            log "INFO" "✅ Iteration successful. Sleeping 60s..."
            sleep 60
        else
            consecutive_failures=$((consecutive_failures + 1))
            log "ERROR" "❌ Iteration failed ($consecutive_failures/$max_failures)"
            
            if [ $consecutive_failures -ge $max_failures ]; then
                log "CRITICAL" "⛔ Too many consecutive failures. Restarting daemon..."
                consecutive_failures=0
                sleep 300  # Wait 5 minutes before restart
            else
                sleep 30  # Short wait between failures
            fi
        fi
    done
}

# Handle signals gracefully
cleanup() {
    log "INFO" "🛑 Daemon stopping (signal received)"
    rm -f "$PID_FILE"
    exit 0
}

trap cleanup INT TERM EXIT

# Start
log "INFO" "🚀 Daemon started with PID $$"
log "INFO" "Logs: $LOG_DIR"
log "INFO" "Friction: $FRICTION_LOG"
log "INFO" ""

# Run forever
main_loop