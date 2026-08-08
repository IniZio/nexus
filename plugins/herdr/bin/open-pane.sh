#!/bin/sh
# open-pane.sh <entrypoint> <placement> — called by herdr actions to open a pane.
ENTRYPOINT="$1"
PLACEMENT="$2"

# Build optional --env arg only when NEXUS3_WORKSPACE is set.
if [ -n "$NEXUS3_WORKSPACE" ]; then
    exec "$HERDR_BIN_PATH" plugin pane open \
        --plugin nexus3 \
        --entrypoint "$ENTRYPOINT" \
        --placement "$PLACEMENT" \
        --focus \
        --workspace "$HERDR_WORKSPACE_ID" \
        --env "NEXUS3_WORKSPACE=$NEXUS3_WORKSPACE"
else
    exec "$HERDR_BIN_PATH" plugin pane open \
        --plugin nexus3 \
        --entrypoint "$ENTRYPOINT" \
        --placement "$PLACEMENT" \
        --focus \
        --workspace "$HERDR_WORKSPACE_ID"
fi
