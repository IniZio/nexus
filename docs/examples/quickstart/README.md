# Quickstart: Your First Nexus Workspace

**Time:** 5 minutes  
**Prerequisites:** Docker, Git

## Problem

You want to start a new project without polluting your local machine with dependencies.

## Setup

### 1. Install Nexus

```bash
# macOS
brew install nexus

# Linux
curl -fsSL https://nexus.dev/install.sh | bash

# Verify
nexus --version
```

### 2. Create Your First Workspace

```bash
# From any git repository
nexus workspace create my-first-workspace
```

This creates:
- A git worktree at `.worktrees/my-first-workspace/`
- A Docker container with your repository
- File sync between host and container

### 3. Start Developing

```bash
# Enter the workspace
nexus workspace ssh my-first-workspace

# You're now inside the container!
$ cat /etc/os-release
# Ubuntu 22.04 LTS

# Install what you need
$ apt-get update && apt-get install -y nodejs npm

# Your code is at /workspace
$ cd /workspace && ls
```

### 4. Exit and Return

```bash
# Exit SSH
$ exit

# Your workspace keeps running
nexus workspace list

# Re-enter anytime
nexus workspace ssh my-first-workspace
```

## Result

You now have:
- ✅ Isolated development environment
- ✅ Your code synced between host and container
- ✅ Ability to install any tools without affecting your host
- ✅ A clean workspace you can destroy and recreate anytime

## Next Steps

- [Node + React Example](../node-react/) - Build a modern web app
- [Fullstack + PostgreSQL](../fullstack-postgres/) - Add a database
