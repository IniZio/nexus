# Migration Guide: Docker Exec to SSH-Based Workspaces

## Overview

Nexus is transitioning from `docker exec` to **SSH-based workspace access**. This change provides:

- ✅ Native SSH agent forwarding (works on macOS)
- ✅ Standard IDE remote development support
- ✅ Familiar SSH workflows
- ✅ Better security model

## What Changed

### Architecture

| Before (Docker Exec) | After (SSH) |
|---------------------|-------------|
| `docker exec` for commands | SSH protocol for all access |
| Platform-specific agent forwarding hacks | Native agent forwarding |
| Limited IDE support | Universal SSH IDE support |
| Custom port forwarding | Native SSH port forwarding (`-L`, `-R`) |
| `docker cp` for file transfer | Standard `scp`/`rsync` |

### CLI Commands

| Old Command | New Command | Notes |
|-------------|-------------|-------|
| `nexus workspace exec ws cmd` | `nexus workspace ssh ws -- cmd` | SSH-based execution |
| `nexus workspace shell ws` | `nexus workspace ssh ws` | Interactive SSH shell |
| `nexus workspace logs ws` | `nexus workspace ssh ws -- docker logs` | Via SSH |
| N/A | `nexus workspace ssh ws -L 3000:localhost:3000` | Port forwarding |

### Configuration

```yaml
# Old configuration (~/.nexus/config.yaml)
secrets:
  ssh:
    mode: agent  # or mount, auto
    
# New configuration
ssh:
  injection:
    enabled: true
    sources:
      - ~/.ssh/id_ed25519.pub
  connection:
    user: nexus
    forward_agent: true
```

## Migration Steps

### 1. Update CLI Tool

```bash
# Install or update to latest nexus CLI
brew upgrade nexus  # macOS
# or
curl -fsSL https://nexus.dev/install.sh | sh
```

### 2. Migrate Existing Workspaces

**Option A: Recreate Workspaces (Recommended)**

```bash
# 1. Note current workspace state
nexus workspace list
nexus workspace show myworkspace

# 2. Stop and destroy old workspace
nexus workspace destroy myworkspace

# 3. Create new SSH-enabled workspace
nexus workspace create myworkspace

# 4. SSH into new workspace
nexus workspace ssh myworkspace
```

**Option B: In-Place Migration**

```bash
# For existing workspaces, Nexus will automatically:
# 1. Install OpenSSH server in container
# 2. Generate host keys
# 3. Inject your public keys
# 4. Configure SSH port

nexus workspace migrate myworkspace

# This command will:
# - Stop the workspace
# - Update container with SSH server
# - Allocate SSH port
# - Restart with SSH enabled
```

### 3. Update SSH Config

```bash
# Generate SSH config for all workspaces
nexus ssh-config generate

# This creates ~/.nexus/ssh_config with entries like:
# Host nexus-feature-auth
#   HostName localhost
#   Port 32801
#   User nexus
#   ForwardAgent yes

# Add to ~/.ssh/config:
echo "Include ~/.nexus/ssh_config" >> ~/.ssh/config
```

### 4. Update IDE Configuration

**VS Code:**

```json
// .vscode/settings.json
{
  "remote.SSH.configFile": "~/.nexus/ssh_config"
}

# Or manually connect:
code --remote ssh-remote+nexus@localhost:32801 /work
```

**Cursor:**

```bash
# Cursor automatically detects Nexus SSH entries
cursor --remote ssh-remote+nexus-feature-auth /work
```

### 5. Update Scripts and Automation

**Before:**
```bash
# Old docker exec scripts
nexus workspace exec myworkspace -- npm test
nexus workspace exec myworkspace -- git status
```

**After:**
```bash
# New SSH-based scripts
nexus workspace ssh myworkspace -- npm test
nexus workspace ssh myworkspace -- git status

# Or use SSH config directly:
ssh nexus-myworkspace -- npm test
```

## Breaking Changes

### 1. Port Allocation

**Before:**
- First port (32768) used for internal exec access
- Service ports started at 32800

**After:**
- SSH port is primary (32800+)
- Service ports follow SSH port

**Impact:** Existing port forwardings may need updating.

### 2. Authentication

**Before:**
- docker exec used root user by default
- No authentication needed (local access)

**After:**
- SSH uses `nexus` user
- Public key authentication required
- Keys injected from `~/.ssh/*.pub`

### 3. File Paths

**Before:**
- Work directory: `/workspace`
- Root access available

**After:**
- Work directory: `/work`
- Non-root user (`nexus`)
- Files synced to `/work`

## Backwards Compatibility

### For Existing Workspaces

Nexus provides a compatibility layer for existing workspaces during the migration period:

```bash
# Old commands still work with deprecation warning
nexus workspace exec myworkspace -- npm test
# ⚠️  Deprecation: Use 'nexus workspace ssh myworkspace -- npm test' instead

# Old commands are translated to SSH equivalents
```

### Migration Timeline

| Phase | Duration | Behavior |
|-------|----------|----------|
| **Phase 1** (Current) | 3 months | Both methods supported, exec deprecated |
| **Phase 2** | 2 months | Exec requires --legacy flag |
| **Phase 3** | 1 month | Exec disabled by default |
| **Phase 4** | Final | Exec removed entirely |

## Troubleshooting

### Connection Refused

```bash
# Check workspace is running
nexus workspace up myworkspace

# Verify SSH port
nexus workspace show myworkspace | grep ssh

# Test connectivity
nexus workspace ssh myworkspace -- -v
```

### Permission Denied (Authentication)

```bash
# Verify keys are injected
nexus workspace ssh myworkspace -- "cat ~/.ssh/authorized_keys"

# Check your public keys exist
ls -la ~/.ssh/*.pub

# Regenerate SSH config
nexus workspace restart myworkspace
```

### Agent Forwarding Not Working

```bash
# Verify agent is running
ssh-add -l

# Check ForwardAgent is enabled
nexus ssh-config show myworkspace | grep ForwardAgent

# Test from within workspace
nexus workspace ssh myworkspace -- "ssh-add -l"
```

### Port Conflicts

```bash
# List all allocated ports
nexus workspace list --ports

# Change SSH port for workspace
nexus workspace config myworkspace --ssh-port 32900
```

## Best Practices

### 1. Use SSH Config

```bash
# Instead of:
ssh -A -p 32801 nexus@localhost

# Use:
ssh nexus-myworkspace
```

### 2. Enable Agent Forwarding

```yaml
# ~/.nexus/config.yaml
ssh:
  connection:
    forward_agent: true
```

### 3. Use SSH for All Workspace Access

```bash
# ✅ Good
nexus workspace ssh myworkspace -- npm test
nexus workspace ssh myworkspace -- git push

# ⚠️ Deprecated
nexus workspace exec myworkspace -- npm test
```

### 4. Configure IDE Integration

```bash
# Generate IDE-specific configs
nexus ide-config generate vscode
nexus ide-config generate cursor
```

## FAQ

**Q: Why switch from docker exec to SSH?**

A: SSH provides native agent forwarding (works on macOS), standard IDE support, better security, and familiar workflows.

**Q: Do I need to recreate all my workspaces?**

A: No, existing workspaces can be migrated in-place with `nexus workspace migrate`.

**Q: Will old scripts break?**

A: Old `workspace exec` commands will work during the deprecation period with warnings. Update to `nexus workspace ssh` for long-term compatibility.

**Q: Is SSH less secure than docker exec?**

A: No, SSH is more secure. It uses key-based authentication, encrypted connections, and proper privilege separation (non-root user).

**Q: Can I still use docker commands directly?**

A: Yes, you can still use `docker exec` if needed, but `nexus workspace ssh` is recommended for consistent behavior and agent forwarding.

**Q: What about Windows/WSL2?**

A: SSH works natively on WSL2. Windows users can use WSL2 or standard SSH clients (PuTTY, Windows Terminal).

## Support

For migration assistance:
- Documentation: https://nexus.dev/docs/ssh-workspaces
- GitHub Issues: https://github.com/nexus-dev/nexus/issues
- Discord: https://discord.gg/nexus
