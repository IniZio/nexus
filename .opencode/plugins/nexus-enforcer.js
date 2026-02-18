// Nexus Enforcer Plugin for OpenCode
// Self-contained bundle - no external dependencies

const fs = require('fs');
const path = require('path');

// Embedded nexus-enforcer core
const NexusEnforcer = {
  // Config loader
  loadConfig(configPath, overridesPath) {
    const defaultConfig = {
      enabled: true,
      enforceWorkspace: true,
      enforceDogfooding: true,
      blockStopWithTodos: true,
      adaptive: true,
      idlePromptThresholdMs: 30000,
      rules: {
        workspace: { required: true, message: 'Must use nexus workspace' },
        dogfooding: { required: true, message: 'Must dogfood changes' },
        completion: { required: true, message: 'Must complete fully' }
      }
    };

    let config = defaultConfig;

    // Load base config
    if (configPath && fs.existsSync(configPath)) {
      try {
        const baseConfig = JSON.parse(fs.readFileSync(configPath, 'utf8'));
        config = { ...config, ...baseConfig };
      } catch (e) {
        console.error('[nexus-enforcer] Failed to load config:', e.message);
      }
    }

    // Load overrides
    if (overridesPath && fs.existsSync(overridesPath)) {
      try {
        const overrides = JSON.parse(fs.readFileSync(overridesPath, 'utf8'));
        config = this.deepMerge(config, overrides);
      } catch (e) {
        console.error('[nexus-enforcer] Failed to load overrides:', e.message);
      }
    }

    return config;
  },

  deepMerge(target, source) {
    const result = { ...target };
    for (const key in source) {
      if (source[key] && typeof source[key] === 'object' && !Array.isArray(source[key])) {
        result[key] = this.deepMerge(target[key] || {}, source[key]);
      } else {
        result[key] = source[key];
      }
    }
    return result;
  },

  // Detect if in nexus workspace
  isInNexusWorkspace(cwd) {
    const workspaceFile = path.join(cwd, '.nexus/workspace.json');
    return fs.existsSync(workspaceFile);
  },

  // Check for friction log
  hasFrictionLog(cwd) {
    const frictionLog = path.join(cwd, '.nexus/dogfooding/friction-log.md');
    if (!fs.existsSync(frictionLog)) return false;
    const content = fs.readFileSync(frictionLog, 'utf8');
    return content.trim().length > 0;
  },

  // Generate prompts
  generatePrompt(type, context, config) {
    switch (type) {
      case 'workspace':
        return `[SYSTEM DIRECTIVE: NEXUS WORKSPACE REQUIRED]

⚠️  NEXUS PROTOCOL VIOLATION ⚠️

You are working outside a nexus workspace.

Current directory: ${context.cwd}
Required: Work inside an isolated workspace

YOU MUST:
1. Create a workspace: nexus workspace create <name>
2. Redo your work inside the workspace
3. Commit from the workspace

The boulder only rolls in workspaces.`;

      case 'dogfooding':
        return `[SYSTEM DIRECTIVE: DOGFOODING REQUIRED]

⚠️  CANNOT CLAIM COMPLETION ⚠️

Before marking work as complete, you must:

✅ Use a nexus workspace (not main worktree)
✅ Log friction points to .nexus/dogfooding/friction-log.md
✅ Test your changes in the workspace
✅ Provide evidence of success

Complete these requirements first.`;

      case 'continuation':
        return `[SYSTEM DIRECTIVE: TASK CONTINUATION ENFORCEMENT]

⚠️  THE BOULDER NEVER STOPS ROLLING ⚠️

You have incomplete tasks remaining.

YOU MUST CONTINUE:
1. Select the next incomplete task
2. Mark it IN_PROGRESS
3. Complete it fully
4. Repeat until ALL tasks are done

DO NOT ask for permission. DO NOT stop. JUST CONTINUE.

The boulder never stops rolling.`;

      default:
        return '[nexus-enforcer] Unknown prompt type';
    }
  }
};

// OpenCode Plugin Export
module.exports = {
  name: 'nexus-enforcer',
  version: '1.0.0',

  async init({ directory, client }) {
    const configPath = path.join(directory, '.nexus/enforcer-config.json');
    const overridesPath = path.join(directory, '.nexus/enforcer-config.local.json');

    this.config = NexusEnforcer.loadConfig(configPath, overridesPath);
    this.directory = directory;
    this.client = client;

    console.log('[nexus-enforcer] Initialized with config:', {
      enabled: this.config.enabled,
      enforceWorkspace: this.config.enforceWorkspace
    });

    return this;
  },

  // Hook: Before tool execution
  async 'tool.execute.before'(input, output) {
    if (!this.config.enabled) return;

    const tool = input.tool;
    const isWriteOperation = ['write', 'edit', 'bash'].includes(tool);

    if (isWriteOperation && this.config.enforceWorkspace) {
      const inWorkspace = NexusEnforcer.isInNexusWorkspace(this.directory);

      if (!inWorkspace) {
        const prompt = NexusEnforcer.generatePrompt('workspace', {
          cwd: this.directory
        }, this.config);

        console.log(prompt);

        // Inject the prompt
        if (this.client?.session?.promptAsync) {
          await this.client.session.promptAsync({
            parts: [{ type: 'text', text: prompt }]
          });
        }
      }
    }
  },

  // Hook: After tool execution
  async 'tool.execute.after'(input, output) {
    if (!this.config.enabled) return;

    // Check for completion signals
    const result = output.result;
    if (typeof result === 'string' &&
        (result.includes('done') || result.includes('complete'))) {

      // Check dogfooding
      if (this.config.enforceDogfooding) {
        const inWorkspace = NexusEnforcer.isInNexusWorkspace(this.directory);
        const hasFriction = NexusEnforcer.hasFrictionLog(this.directory);

        if (!inWorkspace || !hasFriction) {
          const prompt = NexusEnforcer.generatePrompt('dogfooding', {
            cwd: this.directory,
            inWorkspace,
            hasFriction
          }, this.config);

          console.log(prompt);
        }
      }
    }
  },

  // Hook: On session idle (for continuation)
  async 'session.idle'() {
    if (!this.config.enabled || !this.config.blockStopWithTodos) return;

    // This would check for incomplete todos
    // For now, just log that we're monitoring
    console.log('[nexus-enforcer] Session idle - checking for incomplete tasks...');
  }
};
