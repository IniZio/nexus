#!/bin/bash
# Boulder Monitor - Simple file-based idle detection
# Runs independently of OpenCode

STATE_FILE="/home/newman/magic/nexus/.nexus/boulder/state.json"
IDLE_THRESHOLD_MS=30000  # 30 seconds

while true; do
    if [ -f "$STATE_FILE" ]; then
        # Read current state
        LAST_ACTIVITY=$(cat "$STATE_FILE" | grep -o '"lastActivity": [0-9]*' | awk '{print $2}')
        CURRENT_TIME=$(date +%s%3N)
        
        if [ -n "$LAST_ACTIVITY" ]; then
            IDLE_TIME=$((CURRENT_TIME - LAST_ACTIVITY))
            
            if [ $IDLE_TIME -gt $IDLE_THRESHOLD_MS ]; then
                echo "[BOULDER] Idle for ${IDLE_TIME}ms - Enforcement should trigger!"
                echo "Run: cat $STATE_FILE"
            fi
        fi
    fi
    
    # Check every 5 seconds
    sleep 5
done
