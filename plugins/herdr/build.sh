#!/bin/sh
set -e
# NOTE: The live-herdr install / pane-open end-to-end test is DEFERRED and not run in CI (no herdr binary available on the build host).

# Locate nexus3 on PATH.
NEXUS3=$(command -v nexus3 2>/dev/null) || {
    echo "nexus3: error: 'nexus3' not found on PATH" >&2
    echo "Install nexus3 first: https://github.com/IniZio/nexus3#install" >&2
    exit 1
}
NEXUS3=$(readlink -f "$NEXUS3")

# Smoke-test: herdr context-cwd must print the workspace_cwd we pass.
GOT=$(HERDR_PLUGIN_CONTEXT_JSON='{"workspace_cwd":"/"}' "$NEXUS3" herdr context-cwd 2>/dev/null) || {
    echo "nexus3: error: herdr context-cwd probe failed" >&2
    exit 1
}
if [ "$GOT" != "/" ]; then
    echo "nexus3: error: herdr context-cwd returned '$GOT', expected '/'" >&2
    exit 1
fi

# ABI probe: must match the integer in plugins/herdr/abi.
EXPECTED_ABI=$(cat "$(dirname "$0")/abi" 2>/dev/null) || {
    echo "nexus3: error: plugins/herdr/abi not found" >&2
    exit 1
}
GOT_ABI=$("$NEXUS3" herdr abi 2>/dev/null) || {
    echo "nexus3: error: herdr abi probe failed" >&2
    exit 1
}
if [ "$GOT_ABI" != "$EXPECTED_ABI" ]; then
    echo "nexus3: error: ABI mismatch: plugin expects $EXPECTED_ABI, binary reports $GOT_ABI" >&2
    echo "Reinstall: herdr plugin uninstall nexus3 && herdr plugin install <owner>/nexus3/plugins/herdr" >&2
    exit 1
fi

# Write the shim (absolute path so herdr's minimal launchd PATH doesn't matter).
cat > "$(dirname "$0")/nexus3-shim.sh" <<EOF
#!/bin/sh
exec '$NEXUS3' "\$@"
EOF
chmod +x "$(dirname "$0")/nexus3-shim.sh"

echo "nexus3 plugin: shim written -> $(dirname "$0")/nexus3-shim.sh"
