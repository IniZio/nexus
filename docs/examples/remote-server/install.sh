#!/bin/bash
set -e

echo "🚀 Nexus Remote Server Setup"
echo "=============================="

# Check if running as root for systemd setup
if [ "$EUID" -ne 0 ]; then 
  echo "Please run with sudo for systemd setup"
  exit 1
fi

# Detect user
REAL_USER=${SUDO_USER:-$USER}
REAL_HOME=$(eval echo ~$REAL_USER)

echo "Setting up Nexus for user: $REAL_USER"

# Install Nexus if not present
if ! command -v nexus &> /dev/null; then
    echo "Installing Nexus..."
    curl -fsSL https://nexus.dev/install.sh | bash
fi

# Create systemd service
echo "Creating systemd service..."
cat > /etc/systemd/system/nexusd.service << EOF
[Unit]
Description=Nexus Daemon
After=docker.service
Requires=docker.service

[Service]
Type=simple
ExecStart=/usr/local/bin/nexusd
Restart=always
RestartSec=5
User=$REAL_USER
Environment="HOME=$REAL_HOME"

[Install]
WantedBy=multi-user.target
EOF

# Enable and start
echo "Enabling and starting Nexus daemon..."
systemctl daemon-reload
systemctl enable nexusd
systemctl start nexusd

# Check status
echo -e "\n✅ Nexus daemon status:"
systemctl status nexusd --no-pager

echo -e "\n📝 Next steps:"
echo "1. Configure firewall: ufw allow 8080"
echo "2. From local machine: export NEXUS_HOST=$(curl -s ifconfig.me)"
echo "3. Test: nexus status"
