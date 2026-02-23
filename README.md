# Nexus

<p align="center">
  <img src="https://img.shields.io/badge/version-1.0.0-blue.svg" alt="Version">
  <img src="https://img.shields.io/badge/license-MIT-green.svg" alt="License">
  <img src="https://img.shields.io/badge/Node.js-20+-orange.svg" alt="Node.js">
  <img src="https://img.shields.io/badge/Go-1.24+-00ADD8.svg" alt="Go">
</p>

<p align="center">
  <b>AI-native development environment with enforcement, isolation, and traceability</b>
</p>

<p align="center">
  <a href="https://inizio.github.io/nexus">📖 Documentation</a> •
  <a href="#quick-start">🚀 Quick Start</a> •
  <a href="#features">✨ Features</a> •
  <a href="./docs/tutorials/installation.md">🔧 Installation</a>
</p>

---

## The Problem

AI agents write code faster than ever, but they often:
- **Stop prematurely** before tasks are truly complete
- **Work in messy environments** that pollute the main codebase
- **Produce untraceable contributions** with no audit trail
- **Skip quality checks** like tests, linting, and documentation

## The Solution

Nexus makes AI agents **deterministic, traceable, and production-ready** through three integrated components:

### 🏗️ Isolated Workspaces  
Docker-based dev environments with automatic worktree integration. Each task gets a clean, isolated space that won't pollute your main repo.

### 📊 Telemetry & Traces
Following the [Agent Trace](https://agent-trace.dev/) spec for line-level attribution of AI contributions (planned).

### 🎯 Boulder Enforcement ⚠️ Experimental
*For testing/development only - not production-ready*

---

## Quick Start

### Installation

```bash
# Clone the repository
git clone https://github.com/inizio/nexus.git
cd nexus

# Install dependencies
pnpm install

# Build all packages
task build

# Install the CLI
cd packages/nexusd && go build -o nexus ./cmd/cli
sudo mv nexus /usr/local/bin/
```

### Check Boulder Status

```bash
# Check enforcement status
npx @nexus/core boulder status

# Pause with reason
npx @nexus/core boulder pause "Taking a break"

# Resume enforcement
npx @nexus/core boulder resume
```

### Create a Workspace

```bash
# Create isolated workspace
nexus workspace create myproject

# Activate it (auto-intercept mode)
nexus workspace use myproject

# Work normally - commands auto-route to workspace
npm install
npm run dev

# Deactivate
nexus workspace use --clear
```

### Run the Demo

```bash
./scripts/demo.sh
```

---

## Features

<table>
<tr>
<td width="50%">

### 🎯 Boulder Enforcement
- **Idle Detection** - Prevents agents from stopping prematurely
- **Completion Blocking** - Never stops until work is truly done
- **Mini-Workflows** - Enforces docs, git, and CI standards
- **IDE Integration** - Works with OpenCode, Claude Code, Cursor

</td>
<td width="50%">

### 🏗️ Isolated Workspaces
- **Docker-based** - Clean, reproducible environments
- **SSH Access** - Full SSH with agent forwarding
- **Git Worktrees** - Automatic branch isolation
- **Port Management** - Auto-allocated ports (32800-34999)

</td>
</tr>
<tr>
<td width="50%">

### 🔌 IDE Integrations
- **OpenCode** - Native plugin support
- **Claude Code** - Full integration
- **Cursor** - Extension support (in progress)
- **Universal** - Works across editors

</td>
<td width="50%">

### 📊 Telemetry & Traces
- **Agent Trace Spec** - Vendor-neutral format
- **Line-Level Attribution** - Know what code came from AI
- **Conversation Tracking** - Link changes to conversations
- **Query Interface** - Searchable contribution history

</td>
</tr>
</table>

---

## Usage Examples

### Example 1: IDE Integration Setup

```bash
# OpenCode Plugin
cd packages/opencode
pnpm build
mkdir -p ~/.opencode/plugins
cp dist/index.js ~/.opencode/plugins/nexus-enforcer.js

# Claude Code Plugin
cd packages/claude
pnpm build
mkdir -p ~/.claude/plugins
cp dist/index.js ~/.claude/plugins/nexus-enforcer.js

# Cursor Extension
cd packages/cursor
pnpm build
# Load unpacked extension in chrome://extensions/
```

### Example 2: Workspace Workflow

```bash
# Create and activate workspace
nexus workspace create feature-branch
nexus workspace use feature-branch

# Work seamlessly - all commands auto-route
npm install
npm run dev
docker-compose up -d

# Check workspace status
nexus workspace status

# Return to host
nexus workspace use --clear
```

### Example 3: Boulder Enforcement

```bash
# Check current status
npx @nexus/core boulder status

# Configure enforcement
npx @nexus/core boulder config minTasksInQueue 10
npx @nexus/core boulder config idleThresholdMs 120000

# View statistics
npx @nexus/core boulder status
# Output:
# Iteration: 15 | Tasks completed: 42 | In queue: 8
# Status: ACTIVE | Idle: 0s
```

### Example 4: Configuration

```json
// .nexus/enforcer-config.json
{
  "enabled": true,
  "boulder": {
    "enabled": true,
    "idleThresholdMs": 60000,
    "minTasksInQueue": 5
  },
  "rules": {
    "noDirectFileCreation": {
      "enabled": true
    }
  }
}
```

---

## Project Status

> ⚠️ **Note:** The Boulder Enforcement system is **experimental** and intended for testing/development. It may not be stable for production use.

| Component | Status | Description |
|-----------|--------|-------------|
| **Boulder/Enforcer** | ⚠️ Experimental | Task enforcement with idle detection (testing/development only) |
| **Enforcer Core** | ✅ Implemented | Task enforcement with idle detection |
| **OpenCode Plugin** | ✅ Implemented | Full IDE integration |
| **Claude Code** | ✅ Implemented | Full IDE integration |
| **Cursor Extension** | 🚧 In Progress | Extension support |
| **Workspace Daemon** | ✅ Implemented | Docker + SSH workspaces |
| **Workspace CLI** | ✅ Implemented | `nexus workspace` commands |
| **Telemetry** | 📋 Planned | Agent Trace implementation |

---

## Documentation

### For Users
- 📖 [Full Documentation](https://inizio.github.io/nexus)
- 🚀 [Quick Start Guide](./docs/tutorials/workspace-quickstart.md)
- 🔧 [Installation Guide](./docs/tutorials/installation.md)
- 📚 [CLI Reference](./docs/reference/nexus-cli.md)
- ⚙️ [Enforcer Configuration](./docs/reference/enforcer-config.md)

### For Developers
- 🏛️ [Boulder System](./docs/explanation/boulder-system.md) - How enforcement works
- 🤝 [Contributing Guide](./docs/dev/contributing.md)
- 🗺️ [Roadmap](./docs/dev/roadmap.md)
- 📋 [Architecture Decisions](./docs/dev/decisions/)

---

## Philosophy

### Deterministic > Smart

We believe deterministic enforcement beats "smarter" agents:

- **Predictable** - Same input, same enforcement
- **Auditable** - Clear rules, clear violations  
- **Composable** - Mix and match workflows
- **Extensible** - Add custom rules per project

### Mini-Workflows

Rather than relying on agents to "do the right thing," Nexus provides deterministic mini-workflows:

1. **Pre-completion Checklist** - Documentation, tests, CI all passing?
2. **Idle Detection** - No progress? Trigger enforcement.
3. **Quality Gates** - Project-specific conventions enforced automatically

---

## Contributing

We welcome contributions! See [Contributing Guide](./docs/dev/contributing.md) for details.

Key areas where help is needed:
- Additional IDE integrations
- Telemetry implementation (Agent Trace spec)
- Documentation improvements

---

## Resources

- **GitHub:** https://github.com/inizio/nexus
- **Documentation:** https://inizio.github.io/nexus
- **Agent Trace Spec:** https://agent-trace.dev/
- **OpenCode:** https://opencode.ai/

---

## License

MIT License - see [LICENSE](./LICENSE) file for details.

---

<p align="center">
  <b>Nexus:</b> Making AI agents deterministic, traceable, and production-ready.
</p>
