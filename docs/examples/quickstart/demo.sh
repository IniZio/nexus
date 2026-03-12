#!/bin/bash
set -e

echo "🚀 Nexus Quickstart Demo"
echo "========================="

# Create environment
echo "Creating environment..."
nexus environment create quickstart-demo

# Show it's running
echo -e "\n📋 Environment list:"
nexus environment list

# SSH in and run commands
echo -e "\n🔧 Running inside environment:"
nexus environment exec quickstart-demo -- cat /etc/os-release

echo -e "\n✅ Demo complete!"
echo "Enter the environment: nexus environment ssh quickstart-demo"
