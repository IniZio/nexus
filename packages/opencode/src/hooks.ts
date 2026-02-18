import { OpenCodePlugin, createOpenCodePlugin } from './index.js';

let plugin: OpenCodePlugin | null = null;

export function initializePlugin(
  configPath?: string,
  overridesPath?: string
): OpenCodePlugin {
  if (!plugin) {
    plugin = createOpenCodePlugin(configPath, overridesPath);
  }
  return plugin;
}

export function getPlugin(): OpenCodePlugin {
  if (!plugin) {
    throw new Error('Plugin not initialized. Call initializePlugin first.');
  }
  return plugin;
}

export async function onTaskStart(
  taskDescription: string,
  context?: Partial<types.ExecutionContext>
): Promise<types.ValidationResult> {
  const p = getPlugin();
  return p.validateBefore({
    ...context,
    taskDescription,
  });
}

export async function onTaskEnd(
  taskDescription: string,
  context?: Partial<types.ExecutionContext>
): Promise<types.ValidationResult> {
  const p = getPlugin();
  return p.validateAfter({
    ...context,
    taskDescription,
  });
}

export function registerHooks(): void {
  process.on('beforeExit', () => {
    if (plugin) {
      const status = plugin.getStatus();
      console.log('[NEXUS] Final Status:', JSON.stringify(status, null, 2));
    }
  });
}
