# Plugin Setup

This guide covers how to set up Nexus Enforcer plugins for various IDEs and editors.

## OpenCode Plugin

### Installation

1. Build the plugin:
   ```bash
   cd packages/opencode
   npm run build
   ```

2. Copy to plugins directory:
   ```bash
   mkdir -p ~/.opencode/plugins
   cp dist/index.js ~/.opencode/plugins/nexus-enforcer.js
   ```

3. Configure in your project's `.nexus/enforcer-config.json`

### Configuration

```json
{
  "enabled": true,
  "plugin":": "opencode"
  },
  "rules": {
 {
    "type    "noDirectFileCreation": {
      "enabled": true
    }
  }
}
```

## Claude/Claude Code Plugin

### Installation

1. Build the plugin:
   ```bash
   cd packages/claude
   npm run build
   ```

2. Copy to Claude plugins directory:
   ```bash
   mkdir -p ~/.claude/plugins
   cp dist/index.js ~/.claude/plugins/nexus-enforcer.js
   ```

### Configuration

Add to your Claude settings:
```json
{
  "plugins": ["nexus-enforcer"]
}
```

## Cursor Extension

### Installation

1. Build the extension:
   ```bash
   cd packages/cursor
   npm run build
   ```

2. Load unpacked extension in Chrome:
   - Open `chrome://extensions/`
   - Enable Developer mode
   - Click "Load unpacked"
   - Select `packages/cursor/dist`

### Configuration

```json
{
  "nexusEnforcer": {
    "enabled": true,
    "boulderEnabled": true
  }
}
```

## Verifying Installation

### Check OpenCode

```bash
# Start OpenCode and check status
/status
```

You should see `nexus-enforcer` in the loaded plugins list.

### Check Boulder CLI

```bash
boulder status
```

Should show current Boulder state.

## Troubleshooting

### Plugin Not Loading

1. Check file exists: `ls ~/.opencode/plugins/nexus-enforcer.js`
2. Verify ES module format
3. Check console for syntax errors

### Enforcement Not Working

1. Verify `enabled: true` in config
2. Check you're not in an allowed path
3. Check console for error messages

### Build Failures

1. Run `npm install` in package directory
2. Check TypeScript: `npm run build`
3. Verify workspace dependencies
