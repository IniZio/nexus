# Installation

This guide covers how to install and set up Nexus.

## Prerequisites

- **Node.js 20+** - Required for the Enforcer and IDE plugins
- **Go 1.24+** - Required for building the Nexus daemon and CLI
- **Docker** - Required for workspace functionality
- **pnpm** - Package manager (install via `npm install -g pnpm`)

## Quick Install

### 1. Clone the Repository

```bash
git clone https://github.com/inizio/nexus.git
cd nexus
```

### 2. Install Dependencies

```bash
pnpm install
```

### 3. Build All Packages

```bash
task build
```

This builds:
- `@nexus/core` - Enforcer library
- `@nexus/opencode` - OpenCode plugin
- `nexusd` - Workspace daemon and CLI

### 4. Install the CLI

```bash
# Build and install the nexus CLI
cd packages/nexusd
go build -o nexus ./cmd/cli

# Option 1: Add to PATH
cp nexus /usr/local/bin/

# Option 2: Create symlink
ln -s $(pwd)/nexus /usr/local/bin/nexus
```

Or install from source:

```bash
cd packages/nexusd
go install ./cmd/cli
```

## Verification

Check that everything is installed correctly:

```bash
# Check CLI is available
nexus --version

# Check workspace commands
nexus workspace list
```

## IDE Integration

### OpenCode

```bash
# Build the plugin
cd packages/opencode
pnpm build

# Copy to OpenCode plugins directory
mkdir -p ~/.opencode/plugins
cp dist/index.js ~/.opencode/plugins/nexus-enforcer.js
```

### Claude Code

```bash
# Build the plugin
cd packages/claude
pnpm build

# Copy to Claude plugins directory  
mkdir -p ~/.claude/plugins
cp dist/index.js ~/.claude/plugins/nexus-enforcer.js
```

### Cursor

```bash
# Build the extension
cd packages/cursor
pnpm build

# Load unpacked extension in Cursor/Chrome:
# 1. Open chrome://extensions/
# 2. Enable Developer mode
# 3. Click "Load unpacked"
# 4. Select packages/cursor/dist
```

## Next Steps

- [Plugin Setup](plugin-setup.md) - Configure IDE integrations
- [Workspace Quickstart](workspace-quickstart.md) - Create your first workspace

## Troubleshooting

### Build Failures

If `task build` fails:

```bash
# Clean and rebuild
task clean
rm -rf node_modules
pnpm install
task build
```

### Go Build Errors

Ensure you have Go 1.24+:

```bash
go version  # Should show 1.24 or higher
```

If using an older version, update via:
- **macOS**: `brew install go` or `brew upgrade go`
- **Linux**: Download from https://go.dev/dl/

### Permission Denied

If you get permission errors installing the CLI:

```bash
# Use sudo for system-wide install
sudo cp packages/nexusd/nexus /usr/local/bin/

# Or install to user bin
mkdir -p ~/bin
cp packages/nexusd/nexus ~/bin/
export PATH="$HOME/bin:$PATH"
```

### Docker Not Running

Workspace commands require Docker:

```bash
# Check Docker status
docker ps

# Start Docker Desktop or:
sudo systemctl start docker  # Linux
```

## Updating

To update to the latest version:

```bash
git pull
pnpm install
task build
```

Then reinstall the CLI if needed:

```bash
cd packages/nexusd
go build -o nexus ./cmd/cli
cp nexus /usr/local/bin/
```
