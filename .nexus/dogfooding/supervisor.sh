#!/bin/bash
# Nexus Dogfooding Supervisor
# Ensures the daemon never stops running

NEXUS_DIR="/home/newman/magic/nexus-dev/nexus"
DAEMON="$NEXUS_DIR/.nexus/dogfooding/daemon.sh"
PID_FILE="$NEXUS_DIR/.nexus/dogfooding/logs/dogfooding.pid"
LOG_FILE="$NEXUS_DIR/.nexus/dogfooding/logs/supervisor.log"

mkdir -p "$(dirname "$PID_FILE")"

echo "🎛️  Nexus Dogfooding Supervisor"
echo "================================"
echo "Ensures dogfooding never stops"
echo ""

is_daemon_running() {
    if [ -f "$PID_FILE" ]; then
        local pid=$(cat "$PID_FILE")
        if ps -p "$pid" > /dev/null 2>&1; then
            return 0
        fi
    fi
    return 1
}

start_daemon() {
    echo "$(date): Starting daemon..." >> "$LOG_FILE"
    nohup "$DAEMON" >> "$LOG_FILE" 2>&1 &
    sleep 2
    if is_daemon_running; then
        echo "✅ Daemon started (PID: $(cat "$PID_FILE"))"
    else
        echo "❌ Failed to start daemon"
        return 1
    fi
}

stop_daemon() {
    if [ -f "$PID_FILE" ]; then
        local pid=$(cat "$PID_FILE")
        echo "$(date): Stopping daemon (PID: $pid)..." >> "$LOG_FILE"
        kill "$pid" 2>/dev/null || true
        rm -f "$PID_FILE"
        echo "✅ Daemon stopped"
    else
        echo "Daemon not running"
    fi
}

status_daemon() {
    if is_daemon_running; then
        local pid=$(cat "$PID_FILE")
        local iteration=$(cat "$NEXUS_DIR/.nexus/dogfooding/logs/iteration.count" 2>/dev/null || echo "0")
        echo "✅ Daemon running (PID: $pid)"
        echo "   Iterations: $iteration"
        echo "   Log: $LOG_FILE"
    else
        echo "❌ Daemon not running"
    fi
}

# Supervisor loop - restarts daemon if it dies
supervise() {
    echo "🔄 Supervisor mode activated"
    echo "   Watching daemon... (Ctrl+C to stop supervisor)"
    echo ""
    
    while true; do
        if ! is_daemon_running; then
            echo "$(date): Daemon not running, restarting..." | tee -a "$LOG_FILE"
            start_daemon
        fi
        sleep 10  # Check every 10 seconds
    done
}

# Command handling
case "${1:-supervise}" in
    start)
        if is_daemon_running; then
            echo "Daemon already running"
            status_daemon
        else
            start_daemon
        fi
        ;;
    stop)
        stop_daemon
        ;;
    restart)
        stop_daemon
        sleep 2
        start_daemon
        ;;
    status)
        status_daemon
        ;;
    supervise|*)
        supervise
        ;;
esac