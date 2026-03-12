# Quickstart: Your First Nexus Environment

**Time:** 5 minutes  
**Prerequisites:** Docker, Git

## Problem

You want to start a new project without polluting your local machine with dependencies.

## Setup

### 1. Install Nexus

```bash
# Follow docs/tutorials/installation.md
nexus cli-version
```

### 2. Create Your First Environment

```bash
# From any git repository
nexus environment create my-first-environment
```

This creates:
- A git worktree at `.worktrees/my-first-environment/`
- A Docker container with your repository
- File sync between host and container

### 3. Start Developing

```bash
# Enter the environment
nexus environment ssh my-first-environment

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

# Your environment keeps running
nexus environment list

# Re-enter anytime
nexus environment ssh my-first-environment
```

## Result

You now have:
- ✅ Isolated development environment
- ✅ Your code synced between host and container
- ✅ Ability to install any tools without affecting your host
- ✅ A clean environment you can destroy and recreate anytime

## Next Steps

- [Node + React Example](../node-react/) - Build a modern web app
- [Fullstack + PostgreSQL](../fullstack-postgres/) - Add a database
