import type { PluginContext } from './index.js';

export type { PluginContext };

export async function createOpenCodePlugin(
  context: PluginContext
) {
  const { default: NexusOpenCodePlugin } = await import('./index.js');
  return NexusOpenCodePlugin(context);
}

let plugin: ReturnType<typeof createOpenCodePlugin> | null = null;

export async function initializePlugin(
  context: PluginContext
): Promise<ReturnType<typeof createOpenCodePlugin>> {
  if (!plugin) {
    plugin = createOpenCodePlugin(context);
  }
  return plugin;
}

export function getPlugin(): ReturnType<typeof createOpenCodePlugin> {
  if (!plugin) {
    throw new Error('Plugin not initialized. Call initializePlugin first.');
  }
  return plugin;
}

export function registerHooks(): void {
  process.on('beforeExit', () => {
    if (plugin) {
      console.log('[NEXUS] Plugin shutdown');
    }
  });
}
