#!/bin/bash
set -e

echo "🚀 Nexus Quickstart Demo"
echo "========================="

# Create workspace
echo "Creating workspace..."
nexus workspace create quickstart-demo

# Show it's running
echo -e "\n📋 Workspace list:"
nexus workspace list

# SSH in and run commands
echo -e "\n🔧 Running inside workspace:"
nexus workspace exec quickstart-demo -- cat /etc/os-release

echo -e "\n✅ Demo complete!"
echo "Enter the workspace: nexus workspace ssh quickstart-demo"
