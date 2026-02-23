# Nexus

Nexus is an AI-native development environment that makes agent collaboration deterministic, traceable, and production-ready. It combines enforcement mechanisms, isolated workspaces, and telemetry to ensure AI agents deliver consistent, high-quality results.

## Vision

As AI agents write more code, we need systems that ensure:

1. **Deterministic Outcomes** - Agents complete tasks fully and don't stop early
2. **Quality Standards** - Work follows project conventions and passes CI checks
3. **Traceability** - All AI contributions are tracked and attributable
4. **Isolation** - Agents work in clean, reproducible environments

## Components

### 1. Workspace (Implemented)

Inspired by [opencode-devcontainer](https://github.com/athal7/opencode-devcontainer) and [Sprites](https://github.com/peterj/sprites), Workspace provides isolated, reproducible development environments for AI agents.

**Features:**
- Docker-based isolated environments per task/feature
- SSH-based access with agent forwarding
- Git worktree integration
- Port auto-allocation (32800-34999 range)
- `nexus workspace` CLI for management

**Status:** Fully implemented. See [workspace quickstart](tutorials/workspace-quickstart.md).

### 2. Telemetry (Planned)

Following the [Agent Trace](https://agent-trace.dev/) specification, Nexus will track AI contributions with full provenance.

**Vision:**
- Line-level attribution of AI-generated code
- Conversation tracking and linking
- Integration with version control
- Vendor-neutral format
- Queryable contribution history

```typescript
// Example trace record
{
  "version": "0.1.0",
  "files": [{
    "path": "src/utils.ts",
    "conversations": [{
      "url": "https://nexus.dev/conversations/abc123",
      "contributor": {
        "type": "ai",
        "model_id": "anthropic/claude-opus-4-5-20251101"
      },
      "ranges": [{ "start_line": 10, "end_line": 25 }]
    }]
  }]
}
```

## Quick Start

### Installation

```bash
# Clone the repository
git clone https://github.com/inizio/nexus
cd nexus

# Install dependencies
pnpm install

# Build all packages
pnpm run build
```

### IDE Integration

**OpenCode:**
```bash
cp packages/opencode/dist/index.js ~/.opencode/plugins/nexus-enforcer.js
```

**Claude Code:**
See [Claude integration docs](tutorials/plugin-setup.md#claude)

**Cursor:**
See [Cursor integration docs](tutorials/plugin-setup.md#cursor)

## Project Board

| Component | Status | Priority | Documentation |
|-----------|--------|----------|---------------|
| Workspace (nexusd) | ✅ Implemented | High | [Quickstart](tutorials/workspace-quickstart.md) |
| Workspace CLI | ✅ Implemented | High | [CLI](reference/nexus-cli.md) |
| OpenCode Plugin | ✅ Implemented | High | [Setup](tutorials/plugin-setup.md) |
| Claude Integration | ✅ Implemented | High | [Setup](tutorials/plugin-setup.md) |
| Cursor Extension | 🚧 In Progress | Medium | [Setup](tutorials/plugin-setup.md) |
| Telemetry (Agent Trace) | 📋 Planned | Low | - |
| Web Dashboard | 📋 Planned | Low | - |
| Multi-Agent Coordination | 📋 Planned | Low | - |

Legend:
- ✅ Implemented - Ready for use
- ⚠️ Experimental - For testing/development only
- 🚧 In Progress - Under active development
- 📋 Planned - Defined but not started

## Philosophy

### Deterministic > Smart

We believe deterministic enforcement beats "smarter" agents:

- **Predictable** - Same input, same enforcement
- **Auditable** - Clear rules, clear violations
- **Composable** - Mix and match workflows
- **Extensible** - Add custom rules per project

## Documentation

### For Users
- [Plugin Setup](tutorials/plugin-setup.md) - Configure IDE integrations (OpenCode, Claude Code)
- [CLI Reference](reference/nexus-cli.md) - Command reference

### For Developers
- [Contributing](dev/contributing.md) - Development guide
- [Roadmap](dev/roadmap.md) - Future plans
- [Internal Docs](dev/) - Research, plans, and ADRs

## Statistics

| Metric | Value |
|--------|-------|
| Source Code | ~4,273 lines |
| Test Code | ~5,598 lines |
| Test Functions | 153 |
| Test Coverage | 1.3:1 ratio |

## Contributing

We welcome contributions! See [Contributing Guide](dev/contributing.md) for details.

Key areas where help is needed:
- Additional IDE integrations
- Telemetry implementation (Agent Trace spec)
- Additional IDE integrations
- Documentation improvements

## Resources

- **GitHub:** https://github.com/IniZio/nexus
- **Agent Trace Spec:** https://agent-trace.dev/
- **OpenCode:** https://opencode.ai/

## License

MIT License - see LICENSE file for details.

---

**Nexus:** Making AI agents deterministic, traceable, and production-ready.
