# Contributing

## Getting Started

1. Fork the repository
2. Clone your fork:
   ```bash
   git clone https://github.com/YOUR-USERNAME/nexus
   cd nexus
   ```
3. Install dependencies:
   ```bash
   pnpm install
   ```

## Development Workflow

### Local Development

```bash
# Build all packages
pnpm run build

# Run tests
pnpm test

# Start development mode
pnpm run dev
```

### Code Standards

- **Go:** Follow Effective Go and standard Go conventions
- **Tests:** All new code must have tests
- **Documentation:** Update docs for new features

### Running Tests

```bash
# Run all tests
pnpm test

# Run tests for specific package
cd packages/enforcer && pnpm test
```

### Building

```bash
# Build all packages
pnpm run build

# Build specific package
cd packages/opencode && pnpm run build
```

## Architecture

See [Boulder System](../explanation/boulder-system.md) for enforcement architecture details.

## Design Decisions

See [Architecture Decisions](decisions/) for ADRs.

## Submitting Changes

1. Create a feature branch
2. Make changes with tests
3. Run tests: `go test ./...`
4. Submit pull request

## Code Quality Rules

1. Single responsibility: Each function does one thing
2. No premature abstraction
3. Fail fast: Return errors immediately
4. Self-documenting: Clear names > comments
5. Test coverage for all public functions

## Enforcer Development

### Working on the Plugin

```bash
# 1. Edit source (if modifying packages/)
cd /home/newman/magic/nexus-dev/nexus/packages/opencode
# Edit src/index.ts

# 2. Rebuild
cd /home/newman/magic/nexus-dev/nexus
task build

# 3. Copy to plugins directory
cp packages/opencode/dist/index.js .opencode/plugins/nexus-enforcer.js

# 4. Restart OpenCode
# Exit and restart in nexus directory
```

### Testing Changes

```bash
# Quick test in main worktree (should block)
cd /path/to/nexus
# Try: "Create a file test.txt" → Should fail

# Check boulder status
boulder status
```

### Committing Changes

```bash
# All commits must follow Conventional Commits:
# feat: New feature
# fix: Bug fix
# docs: Documentation
# refactor: Code refactoring
# test: Tests
# chore: Maintenance

# Example:
git commit -m "feat(opencode): Add friction log reminder"
```

## Key Files to Know

| File | Purpose | When to Edit |
|------|---------|--------------|
| `.opencode/plugins/nexus-enforcer.js` | **ACTIVE PLUGIN** | Immediate fixes |
| `packages/opencode/src/` | Plugin source | Feature development |
| `packages/enforcer/src/` | Core library | Shared logic |
| `.nexus/enforcer-config.json` | Base rules | Project defaults |
| `.nexus/enforcer-config.local.json` | Local overrides | Personal preferences |
| `opencode.json` | OpenCode config | Rarely (plugin list) |
| `Taskfile.yml` | Build tasks | New build steps |

## Common Issues & Solutions

### Plugin Not Loading

**Symptoms:** `/status` doesn't show nexus-enforcer

**Solutions:**
1. Check file is at `.opencode/plugins/nexus-enforcer.js`
2. Verify ES module format (`export default` not `module.exports`)
3. Restart OpenCode
4. Check OpenCode console for syntax errors

### Enforcement Not Working

**Symptoms:** Can write files without workspace

**Solutions:**
1. Check `.nexus/enforcer-config.json` has `"enabled": true`
2. Verify you're not in an allowed path (some paths are whitelisted)
3. Check OpenCode console for error messages

### Build Failures

**Symptoms:** `task build` fails

**Solutions:**
1. Run `pnpm install` in each package directory
2. Check TypeScript errors: `pnpm run build` in package dir
3. Verify workspace dependencies use `workspace:*` not versions
