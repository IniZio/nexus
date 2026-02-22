#!/bin/bash
#
# SSH Agent Bridge for macOS Docker
# 
# This script bridges SSH agent from macOS host to Docker containers.
# Required because Docker Desktop on macOS runs containers in a Linux VM,
# which prevents direct Unix socket bind mounting.
#
# Usage:
#   1. Start bridge: ./ssh-agent-bridge.sh
#   2. Run container with SSH_AGENT_BRIDGE_PORT environment variable
#   3. Container entrypoint must bridge TCP back to Unix socket
#
# Installation:
#   brew install socat
#

set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Configuration
BRIDGE_PORT_FILE="${HOME}/.nexus/ssh-bridge-port"
PID_FILE="${HOME}/.nexus/ssh-bridge.pid"

# Find SSH agent socket
find_ssh_agent_socket() {
    # Check environment variable first
    if [ -n "${SSH_AUTH_SOCK:-}" ]; then
        if [ -S "$SSH_AUTH_SOCK" ]; then
            echo "$SSH_AUTH_SOCK"
            return 0
        fi
    fi
    
    # Try macOS launchd sockets
    for socket in /tmp/com.apple.launchd.*/Listeners; do
        if [ -S "$socket" ]; then
            echo "$socket"
            return 0
        fi
    done
    
    # Try standard ssh-agent sockets
    for socket in /tmp/ssh-*/agent.*; do
        if [ -S "$socket" ]; then
            echo "$socket"
            return 0
        fi
    done
    
    return 1
}

# Check prerequisites
check_prerequisites() {
    if ! command -v socat &> /dev/null; then
        echo -e "${RED}Error: socat is not installed${NC}"
        echo "Install with: brew install socat"
        exit 1
    fi
    
    SSH_AUTH_SOCK=$(find_ssh_agent_socket)
    if [ -z "$SSH_AUTH_SOCK" ]; then
        echo -e "${RED}Error: SSH agent not found${NC}"
        echo "Start ssh-agent with: eval \$(ssh-agent -s)"
        echo "Then add your key: ssh-add ~/.ssh/id_ed25519"
        exit 1
    fi
    
    echo -e "${GREEN}✓ Found SSH agent at: $SSH_AUTH_SOCK${NC}"
}

# Find available port
find_available_port() {
    local port
    # Use Python to find an available port
    if command -v python3 &> /dev/null; then
        port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')
    elif command -v python &> /dev/null; then
        port=$(python -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')
    else
        # Fallback: random port between 10000-65000
        port=$((RANDOM % 55000 + 10000))
    fi
    echo "$port"
}

# Start the bridge
start_bridge() {
    check_prerequisites
    
    # Check if already running
    if [ -f "$PID_FILE" ]; then
        local old_pid
        old_pid=$(cat "$PID_FILE")
        if kill -0 "$old_pid" 2>/dev/null; then
            echo -e "${YELLOW}Bridge already running (PID: $old_pid)${NC}"
            if [ -f "$BRIDGE_PORT_FILE" ]; then
                local old_port
                old_port=$(cat "$BRIDGE_PORT_FILE")
                echo "Port: $old_port"
                echo ""
                echo "Export for containers:"
                echo "  export SSH_AGENT_BRIDGE_PORT=$old_port"
            fi
            exit 0
        else
            # Stale PID file
            rm -f "$PID_FILE" "$BRIDGE_PORT_FILE"
        fi
    fi
    
    # Create directory for state files
    mkdir -p "$(dirname "$PID_FILE")"
    
    # Find available port
    local port
    port=$(find_available_port)
    
    echo "Starting SSH agent bridge on port $port..."
    
    # Start socat in background
    # TCP-LISTEN: listens on localhost
    # fork: handle multiple connections
    # reuseaddr: allow socket reuse
    # range=127.0.0.1/32: only accept localhost connections (security)
    # UNIX-CONNECT: connects to SSH agent socket
    socat TCP-LISTEN:"$port",fork,reuseaddr,range=127.0.0.1/32 \
          UNIX-CONNECT:"$SSH_AUTH_SOCK" &
    
    local socat_pid=$!
    
    # Wait a moment for socat to start
    sleep 0.5
    
    # Verify socat started successfully
    if ! kill -0 $socat_pid 2>/dev/null; then
        echo -e "${RED}Error: Failed to start socat${NC}"
        exit 1
    fi
    
    # Save PID and port
    echo $socat_pid > "$PID_FILE"
    echo $port > "$BRIDGE_PORT_FILE"
    
    echo -e "${GREEN}✓ Bridge started (PID: $socat_pid)${NC}"
    echo ""
    echo "Export for containers:"
    echo -e "  ${GREEN}export SSH_AGENT_BRIDGE_PORT=$port${NC}"
    echo ""
    echo "Run container:"
    echo "  docker run -e SSH_AGENT_BRIDGE_PORT=$port \\"
    echo "             -e SSH_AUTH_SOCK=/ssh-agent \\"
    echo "             your-image"
    echo ""
    echo "Stop bridge: $0 stop"
}

# Stop the bridge
stop_bridge() {
    if [ -f "$PID_FILE" ]; then
        local pid
        pid=$(cat "$PID_FILE")
        if kill -0 "$pid" 2>/dev/null; then
            echo "Stopping bridge (PID: $pid)..."
            kill "$pid" 2>/dev/null || true
            sleep 0.5
            # Force kill if still running
            if kill -0 "$pid" 2>/dev/null; then
                kill -9 "$pid" 2>/dev/null || true
            fi
            echo -e "${GREEN}✓ Bridge stopped${NC}"
        else
            echo "Bridge not running"
        fi
        rm -f "$PID_FILE" "$BRIDGE_PORT_FILE"
    else
        echo "Bridge not running"
    fi
}

# Check bridge status
status_bridge() {
    if [ -f "$PID_FILE" ]; then
        local pid
        pid=$(cat "$PID_FILE")
        if kill -0 "$pid" 2>/dev/null; then
            local port
            port=$(cat "$BRIDGE_PORT_FILE" 2>/dev/null || echo "unknown")
            echo -e "${GREEN}Bridge is running${NC}"
            echo "  PID: $pid"
            echo "  Port: $port"
            echo ""
            echo "Export for containers:"
            echo "  export SSH_AGENT_BRIDGE_PORT=$port"
        else
            echo -e "${YELLOW}Bridge not running (stale PID file)${NC}"
            rm -f "$PID_FILE" "$BRIDGE_PORT_FILE"
        fi
    else
        echo "Bridge not running"
    fi
}

# Test the bridge
test_bridge() {
    if [ -f "$BRIDGE_PORT_FILE" ]; then
        local port
        port=$(cat "$BRIDGE_PORT_FILE")
        
        echo "Testing SSH agent bridge on port $port..."
        
        # Run test container
        docker run --rm \
            -e SSH_AGENT_BRIDGE_PORT="$port" \
            -e SSH_AUTH_SOCK=/ssh-agent \
            alpine/socat:latest \
            sh -c "
                # Bridge TCP to Unix socket
                socat UNIX-LISTEN:/ssh-agent,fork TCP:host.docker.internal:$port &
                sleep 1
                # Test with ssh-add -l
                SSH_AUTH_SOCK=/ssh-agent ssh-add -l
            " 2>&1 && {
            echo -e "${GREEN}✓ Bridge test successful${NC}"
        } || {
            echo -e "${RED}✗ Bridge test failed${NC}"
            exit 1
        }
    else
        echo -e "${RED}Bridge not running${NC}"
        exit 1
    fi
}

# Show help
show_help() {
    cat << EOF
SSH Agent Bridge for macOS Docker

This script bridges SSH agent from macOS host to Docker containers.
Required because Docker Desktop on macOS runs containers in a Linux VM.

Usage: $0 [command]

Commands:
  start    Start the SSH agent bridge (default)
  stop     Stop the SSH agent bridge
  status   Check bridge status
  test     Test the bridge with a Docker container
  help     Show this help message

Examples:
  # Start bridge
  $0 start

  # Run container with SSH forwarding
  export SSH_AGENT_BRIDGE_PORT=\$(cat ~/.nexus/ssh-bridge-port)
  docker run -e SSH_AGENT_BRIDGE_PORT=\$SSH_AGENT_BRIDGE_PORT \\
             -e SSH_AUTH_SOCK=/ssh-agent \\
             your-image

  # Stop bridge
  $0 stop

Prerequisites:
  brew install socat

Note:
  The bridge binds to localhost (127.0.0.1) only for security.
  Random ephemeral ports are used to avoid conflicts.
EOF
}

# Main
main() {
    case "${1:-start}" in
        start)
            start_bridge
            ;;
        stop)
            stop_bridge
            ;;
        status)
            status_bridge
            ;;
        test)
            test_bridge
            ;;
        help|--help|-h)
            show_help
            ;;
        *)
            echo "Unknown command: $1"
            show_help
            exit 1
            ;;
    esac
}

main "$@"
