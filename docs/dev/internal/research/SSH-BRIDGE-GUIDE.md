# SSH Agent Forwarding with Docker on macOS

## Quick Start

This guide shows how to use SSH agent forwarding with Docker containers on macOS.

### Prerequisites

```bash
# Install socat (required for TCP bridge)
brew install socat

# Ensure SSH agent is running and has your keys
ssh-add -l  # Should list your keys
# If not, add them:
ssh-add ~/.ssh/id_ed25519
```

### Method 1: Using the Bridge Script (Recommended)

**Step 1: Start the SSH agent bridge on your Mac**

```bash
# Download and run the bridge script
chmod +x ssh-agent-bridge.sh
./ssh-agent-bridge.sh start

# Output:
# ✓ Found SSH agent at: /tmp/com.apple.launchd.xxx/Listeners
# Starting SSH agent bridge on port 54321...
# ✓ Bridge started (PID: 12345)
#
# Export for containers:
#   export SSH_AGENT_BRIDGE_PORT=54321
```

**Step 2: Run your container with the bridge port**

```bash
export SSH_AGENT_BRIDGE_PORT=$(cat ~/.nexus/ssh-bridge-port)

docker run -it \
  -e SSH_AGENT_BRIDGE_PORT=$SSH_AGENT_BRIDGE_PORT \
  -e SSH_AUTH_SOCK=/ssh-agent \
  --entrypoint /usr/local/bin/nexus-ssh-setup \
  your-image \
  bash
```

**Step 3: Test SSH inside the container**

```bash
# Inside container:
ssh-add -l  # Should show your keys
ssh -T git@github.com  # Should authenticate successfully
git clone git@github.com:your-org/private-repo.git
```

**Step 4: Stop the bridge when done**

```bash
./ssh-agent-bridge.sh stop
```

### Method 2: Manual Setup

**Step 1: Find your SSH agent socket**

```bash
echo $SSH_AUTH_SOCK
# Output: /tmp/com.apple.launchd.xxx/Listeners
```

**Step 2: Start socat bridge manually**

```bash
# Terminal 1: Start the bridge
socat TCP-LISTEN:10000,fork,reuseaddr,range=127.0.0.1/32 \
      UNIX-CONNECT:$SSH_AUTH_SOCK
```

**Step 3: Run container with bridge**

```bash
# Terminal 2: Run container
docker run -it \
  -e SSH_AGENT_BRIDGE_PORT=10000 \
  -e SSH_AUTH_SOCK=/ssh-agent \
  alpine/socat sh -c "
    socat UNIX-LISTEN:/ssh-agent,fork TCP:host.docker.internal:10000 &
    sleep 1
    SSH_AUTH_SOCK=/ssh-agent ssh -T git@github.com
  "
```

### Method 3: Without socat (Key Mounting - Less Secure)

If you can't install socat, mount SSH keys directly:

```bash
docker run -it \
  -v ~/.ssh:/root/.ssh:ro \
  -e GIT_SSH_COMMAND="ssh -i /root/.ssh/id_ed25519 -o StrictHostKeyChecking=accept-new" \
  your-image bash
```

**Security note**: This exposes your keys to the container. Use agent forwarding when possible.

---

## Docker Image Setup

Add the SSH setup script to your Docker image:

```dockerfile
FROM node:18

# Install socat for SSH agent bridging
RUN apt-get update && apt-get install -y socat openssh-client git \
    && rm -rf /var/lib/apt/lists/*

# Copy the SSH setup script
COPY nexus-ssh-setup.sh /usr/local/bin/nexus-ssh-setup
RUN chmod +x /usr/local/bin/nexus-ssh-setup

# Use the SSH setup script as entrypoint
ENTRYPOINT ["/usr/local/bin/nexus-ssh-setup"]
CMD ["bash"]
```

Build and run:

```bash
docker build -t my-dev-image .

export SSH_AGENT_BRIDGE_PORT=$(cat ~/.nexus/ssh-bridge-port)
docker run -it \
  -e SSH_AGENT_BRIDGE_PORT=$SSH_AGENT_BRIDGE_PORT \
  -e SSH_AUTH_SOCK=/ssh-agent \
  my-dev-image
```

---

## Docker Compose Setup

```yaml
version: '3.8'

services:
  dev:
    build: .
    environment:
      - SSH_AGENT_BRIDGE_PORT=${SSH_AGENT_BRIDGE_PORT}
      - SSH_AUTH_SOCK=/ssh-agent
    volumes:
      - .:/workspace
    working_dir: /workspace
    command: ["/usr/local/bin/nexus-ssh-setup", "bash"]
```

Usage:

```bash
# Start bridge
./ssh-agent-bridge.sh start

# Export port and run compose
export SSH_AGENT_BRIDGE_PORT=$(cat ~/.nexus/ssh-bridge-port)
docker-compose up -d dev
docker-compose exec dev bash
```

---

## Troubleshooting

### "SSH agent not found"

```bash
# Start ssh-agent
 eval $(ssh-agent -s)

# Add your key
ssh-add ~/.ssh/id_ed25519

# Verify
ssh-add -l
```

### "Connection refused" inside container

```bash
# Check bridge is running
./ssh-agent-bridge.sh status

# Test the bridge
./ssh-agent-bridge.sh test

# Check host.docker.internal resolves
docker run --rm alpine ping -c 1 host.docker.internal
```

### "Permission denied (publickey)"

```bash
# Inside container, check SSH_AUTH_SOCK is set
echo $SSH_AUTH_SOCK

# Check keys are available
ssh-add -l

# Test verbose SSH
ssh -vT git@github.com
```

### Bridge not working with Docker Desktop 4.34+

Docker Desktop has changed networking. Ensure:

1. You're using `host.docker.internal` (not `host.docker.internal` on older versions)
2. Your container can resolve the hostname:
   ```bash
   docker run --rm alpine nslookup host.docker.internal
   ```

---

## Security Considerations

### TCP Bridge Security

The TCP bridge uses these security measures:

1. **Localhost-only binding**: Socat binds to `127.0.0.1` only
2. **Random high port**: Ephemeral port selection (10000-65000)
3. **Agent protocol**: Only SSH agent protocol flows over TCP, not key material
4. **Short-lived**: Bridge exists only while workspace runs

### Comparison of Methods

| Method | Security | Keys in Container | Passphrase | Setup Complexity |
|--------|----------|-------------------|------------|------------------|
| TCP Bridge | 🔒 High | ❌ No | ✅ Yes | Medium |
| Key Mount | 🔐 Medium | ✅ Yes | ❌ No | Low |
| Key Mount + Tmpfs | 🔐 Medium | ⚠️ Memory only | ❌ No | Medium |

### Best Practices

1. **Use TCP bridge** (with socat) for development
2. **Rotate keys regularly** using `ssh-add -D` and re-add
3. **Stop bridge** when not needed
4. **Use key confirmation** for sensitive keys:
   ```bash
   ssh-add -c ~/.ssh/id_ed25519  # Prompts before each use
   ```

---

## How It Works

### macOS Docker Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                     macOS Host                              │
│  ┌───────────────────────────────────────────────────────┐  │
│  │  SSH Agent (ssh-agent)                                 │  │
│  │  Unix socket: /tmp/ssh-XXXXXX/agent.XXXXXX            │  │
│  └──────────┬────────────────────────────────────────────┘  │
│             │ Unix socket                                    │
│             ▼                                                │
│  ┌───────────────────────────────────────────────────────┐  │
│  │  socat (this bridge)                                   │  │
│  │  Forwards: Unix socket → TCP localhost:PORT           │  │
│  └──────────┬────────────────────────────────────────────┘  │
│             │ TCP (localhost only)                           │
│             ▼                                                │
│  ┌───────────────────────────────────────────────────────┐  │
│  │  Docker Desktop VM (Linux)                            │  │
│  │  Network layer forwards TCP                           │  │
│  └──────────┬────────────────────────────────────────────┘  │
│             │ TCP (host.docker.internal)                     │
│             ▼                                                │
│  ┌───────────────────────────────────────────────────────┐  │
│  │  Docker Container                                      │  │
│  │  ┌─────────────────────────────────────────────────┐  │  │
│  │  │ socat bridges TCP → Unix socket /ssh-agent      │  │  │
│  │  │ SSH clients use /ssh-agent                      │  │  │
│  │  └─────────────────────────────────────────────────┘  │  │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

### Why This Is Needed

Docker Desktop on macOS uses a Linux VM. The VM's file sharing (virtiofs, gRPC FUSE, osxfs) doesn't support Unix sockets. Therefore:

- ❌ Direct socket mounting: `docker run -v $SSH_AUTH_SOCK:/ssh-agent` **FAILS**
- ✅ TCP bridging: Unix socket → TCP → Unix socket **WORKS**

---

## References

- [SSH Agent Protocol](https://tools.ietf.org/html/draft-ietf-secsh-agent-02)
- [Docker Desktop macOS Networking](https://docs.docker.com/desktop/features/networking/)
- [socat Documentation](http://www.dest-unreach.org/socat/)
- [Nexus SSH Research Document](./ssh-agent-macos-docker.md)
