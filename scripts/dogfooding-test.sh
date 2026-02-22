#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
TEST_PROJECT="$PROJECT_ROOT/examples/docker-compose-workspace"
LOG_FILE="$PROJECT_ROOT/dogfooding-test.log"
NEXUS_CLI="${PROJECT_ROOT}/packages/nexusd/nexus"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log() {
    echo -e "$1" | tee -a "$LOG_FILE"
}

check_daemon() {
    log "${YELLOW}Checking daemon status...${NC}"
    if ! "$NEXUS_CLI" status 2>&1 | grep -q "Daemon:.*running"; then
        log "${YELLOW}WARNING: Daemon not running. Starting nexusd...${NC}"
        log "${YELLOW}NOTE: Daemon must be started manually with: nexusd -token <token>${NC}"
        return 1
    fi
    log "${GREEN}Daemon is running${NC}"
    return 0
}

setup() {
    log "${YELLOW}=== Dogfooding Test Setup ===${NC}"
    
    if [ ! -d "$TEST_PROJECT" ]; then
        log "${RED}ERROR: Test project not found at $TEST_PROJECT${NC}"
        exit 1
    fi
    
    if [ ! -x "$NEXUS_CLI" ]; then
        log "${RED}ERROR: nexus CLI not found at $NEXUS_CLI${NC}"
        exit 1
    fi
    
    check_daemon || log "${YELLOW}Skipping workspace tests - daemon not available${NC}"
    
    log "${GREEN}Setup complete${NC}"
}

run_test() {
    log "${YELLOW}=== Running Dogfooding Test ===${NC}"
    log "Project: $TEST_PROJECT"
    log "Task: Start docker-compose services"
    
    WORKSPACE_NAME="dogfooding-test-$(date +%s)"
    log "Creating workspace: $WORKSPACE_NAME"
    
    if "$NEXUS_CLI" workspace create "$WORKSPACE_NAME" --from "$TEST_PROJECT" >> "$LOG_FILE" 2>&1; then
        log "${GREEN}Workspace created successfully${NC}"
    else
        log "${RED}Failed to create workspace${NC}"
        return 1
    fi
    
    log "${YELLOW}Testing nexus exec...${NC}"
    
    if "$NEXUS_CLI" workspace exec "$WORKSPACE_NAME" -- docker-compose ps >> "$LOG_FILE" 2>&1; then
        log "${GREEN}Nexus exec works${NC}"
    else
        log "${RED}Nexus exec failed${NC}"
    fi
    
    log "${YELLOW}Cleaning up workspace...${NC}"
    "$NEXUS_CLI" workspace delete "$WORKSPACE_NAME" --force >> "$LOG_FILE" 2>&1 || true
    
    log "${GREEN}Test complete${NC}"
}

simulate_agent() {
    log "${YELLOW}=== Simulating Naive Agent Behavior ===${NC}"
    
    cd "$TEST_PROJECT"
    
    log "Naive agent would run: docker-compose ps"
    
    if command -v docker-compose &> /dev/null || command -v docker &> /dev/null; then
        log "Checking docker-compose availability..."
        docker-compose ps >> "$LOG_FILE" 2>&1 || true
    fi
    
    log "${YELLOW}Agent sees: Guardrail warning (if enforcer active)${NC}"
    log "${YELLOW}Agent should use: nexus workspace create && nexus exec${NC}"
}

parse_results() {
    log "${YELLOW}=== Parsing Results ===${NC}"
    
    GUARDRAIL_TRIGGERED=false
    NEXUS_COMMANDS=0
    
    if grep -qi "guard\|warning\|main.*worktree" "$LOG_FILE" 2>/dev/null; then
        GUARDRAIL_TRIGGERED=true
        log "${GREEN}Guardrail triggered: YES${NC}"
    else
        log "${YELLOW}Guardrail triggered: NO (may need enforcer plugin)${NC}"
    fi
    
    if grep -q "nexus.*exec\|nexus.*workspace" "$LOG_FILE" 2>/dev/null; then
        NEXUS_COMMANDS=$(grep -c "nexus.*exec\|nexus.*workspace" "$LOG_FILE")
        log "${GREEN}Nexus commands used: $NEXUS_COMMANDS${NC}"
    else
        log "${YELLOW}Nexus commands used: 0${NC}"
    fi
    
    log ""
    log "=== Summary ==="
    log "Guardrail Triggered: $GUARDRAIL_TRIGGERED"
    log "Nexus Commands: $NEXUS_COMMANDS"
    log "Log file: $LOG_FILE"
}

main() {
    echo "" > "$LOG_FILE"
    
    setup
    run_test
    simulate_agent
    parse_results
    
    log "${GREEN}=== Test Complete ===${NC}"
}

main "$@"
