# Remote Server Workspace

**Time:** 30 minutes  
**Use Case:** Run Nexus on cloud/remote infrastructure

## Problem

Your local machine lacks:
- Sufficient RAM/CPU for large projects
- Persistent 24/7 availability
- Fast network for large downloads
- Ability to work from multiple devices

## Solution

Run `nexusd` (Nexus daemon) on a remote server (AWS, GCP, VPS, homelab) and connect from anywhere.

## Architecture

```
┌──────────────┐         ┌──────────────────┐
│   Laptop     │  SSH    │   Remote Server  │
│  (nexus CLI) │◄───────►│   (nexusd)       │
└──────────────┘         └────────┬─────────┘
                                  │
                    ┌─────────────┼─────────────┐
                    ▼             ▼             ▼
              ┌─────────┐  ┌─────────┐  ┌─────────┐
              │Docker   │  │Git      │  │File     │
              │Container│  │Worktree │  │Sync     │
              └─────────┘  └─────────┘  └─────────┘
```

## Setup

### Step 1: Provision Remote Server

Requirements:
- Ubuntu 22.04 LTS (recommended)
- 4+ CPU cores
- 8GB+ RAM
- 50GB+ disk
- Docker installed

Example: AWS EC2 t3.large, DigitalOcean droplet, or Hetzner CX31

### Step 2: Install Nexus on Server

SSH into your remote server:

```bash
# SSH to remote server
ssh user@your-server-ip

# Install Nexus
curl -fsSL https://nexus.dev/install.sh | bash

# Verify installation
nexus --version
```

### Step 3: Configure Nexus Daemon

Create systemd service for persistent operation:

```bash
# Create service file
sudo tee /etc/systemd/system/nexusd.service << 'EOF'
[Unit]
Description=Nexus Daemon
After=docker.service
Requires=docker.service

[Service]
Type=simple
ExecStart=/usr/local/bin/nexusd
Restart=always
RestartSec=5
User=ubuntu
Environment="HOME=/home/ubuntu"

[Install]
WantedBy=multi-user.target
EOF

# Reload and start
sudo systemctl daemon-reload
sudo systemctl enable nexusd
sudo systemctl start nexusd

# Check status
sudo systemctl status nexusd
```

### Step 4: Configure Local CLI

On your local machine:

```bash
# Point Nexus to remote server
export NEXUS_HOST=your-server-ip
export NEXUS_PORT=8080

# Or use config file
mkdir -p ~/.config/nexus
cat > ~/.config/nexus/config.yaml << EOF
server:
  host: your-server-ip
  port: 8080
EOF

# Test connection
nexus status
```

### Step 5: Create Remote Workspace

```bash
# Create workspace on remote server
nexus workspace create remote-dev

# This creates the workspace on the REMOTE server
# Files are synced back to your local machine
```

## SSH Key Authentication

For passwordless access:

```bash
# Copy your SSH key to remote server
ssh-copy-id user@your-server-ip

# Configure Nexus to use key
nexus workspace inject-key remote-dev
```

## Development Workflow

```bash
# 1. Connect to remote workspace
nexus workspace ssh remote-dev

# 2. Work normally - all compute happens remotely
$ npm install
$ npm run build
$ docker-compose up -d

# 3. Exit - workspace keeps running
$ exit

# 4. Check status from anywhere
nexus workspace status remote-dev
```

## Port Forwarding

Access services running on remote workspace locally:

```bash
# Forward remote port 3000 to local port 3000
nexus workspace port add remote-dev 3000

# Now access http://localhost:3000 on your laptop
# Requests go to remote workspace!
```

## Multi-Device Workflow

```
Laptop at office:
  nexus workspace ssh remote-dev
  # do some work
  exit

Laptop at home:
  nexus workspace ssh remote-dev
  # continue where you left off
  # all state preserved on remote
```

## Security Considerations

1. **Firewall**: Only expose Nexus port to your IP
   ```bash
   sudo ufw allow from YOUR_IP to any port 8080
   ```

2. **Authentication**: Use API tokens
   ```bash
   nexus auth login
   ```

3. **SSH**: Always use key-based auth

4. **Updates**: Keep Nexus updated
   ```bash
   nexus update
   ```

## Result

✅ **What you achieved:**
- Development environment accessible from anywhere
- Consistent performance regardless of local machine
- Work persists across device switches
- Can use lightweight laptop for heavy compute tasks
- 24/7 available development environment

## Troubleshooting

### Connection refused
```bash
# Check if daemon is running on server
ssh user@server "sudo systemctl status nexusd"

# Check firewall
ssh user@server "sudo ufw status"
```

### Slow sync
```bash
# Check sync status
nexus sync status remote-dev

# Pause sync for large operations
nexus sync pause remote-dev
# ... do work ...
nexus sync resume remote-dev
```

## Cleanup

```bash
# Delete remote workspace
nexus workspace delete remote-dev

# Stop daemon on server
ssh user@server "sudo systemctl stop nexusd"
```
