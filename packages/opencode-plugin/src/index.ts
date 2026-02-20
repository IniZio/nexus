import type { PluginContext, Plugin } from './types/opencode-sdk';
import { WorkspaceClient } from './types/workspace-sdk';
import { loadConfig, validateConfig, NexusConfig } from './config';
import { initToolIntercept, registerToolHooks } from './hooks/tool-intercept';
import { initActivityTracking, stopActivityTracking } from './hooks/activity';
import { connectCommand, statusCommand, disconnectCommand, isConnected } from './commands/nexus';

let workspaceClient: WorkspaceClient | null = null;
let config: NexusConfig | null = null;

export interface NexusPluginOptions {
  configPath?: string;
  autoConnect?: boolean;
}

export async function initialize(options: NexusPluginOptions = {}): Promise<void> {
  try {
    config = loadConfig(options.configPath);
    validateConfig(config);

    if (options.autoConnect) {
      workspaceClient = new WorkspaceClient({
        endpoint: config.workspace.endpoint,
        workspaceId: config.workspace.workspaceId,
        token: config.workspace.token,
      });

      await workspaceClient.connect();
      await workspaceClient.setStatus('active');

      initActivityTracking(workspaceClient, {
        idleTimeout: config.options?.idleTimeout,
        keepAliveInterval: config.options?.keepAliveInterval,
      });

      initToolIntercept(workspaceClient, config);
    }
  } catch (error) {
    console.error('[nexus] Failed to initialize plugin:', error);
    throw error;
  }
}

export async function shutdown(): Promise<void> {
  stopActivityTracking();

  if (workspaceClient) {
    await workspaceClient.setStatus('disconnected');
    await workspaceClient.disconnect();
    workspaceClient = null;
  }
}

export function createPlugin(): Plugin {
  return {
    name: '@nexus/opencode-plugin',
    version: '0.1.0',

    async onLoad(context: PluginContext): Promise<void> {
      context.ui.showNotification('Nexus Workspace Plugin loaded', 'info');

      try {
        const nexusConfig = loadConfig();
        validateConfig(nexusConfig);

        workspaceClient = new WorkspaceClient({
          endpoint: nexusConfig.workspace.endpoint,
          workspaceId: nexusConfig.workspace.workspaceId,
          token: nexusConfig.workspace.token,
        });

        await workspaceClient.connect();
        await workspaceClient.setStatus('active');

        initActivityTracking(workspaceClient, {
          idleTimeout: nexusConfig.options?.idleTimeout,
          keepAliveInterval: nexusConfig.options?.keepAliveInterval,
        });

        initToolIntercept(workspaceClient, nexusConfig);
        registerToolHooks(context);

        context.ui.showNotification('Connected to Nexus Workspace', 'success');
      } catch (error) {
        const message = error instanceof Error ? error.message : 'Unknown error';
        context.ui.showNotification(`Nexus plugin init failed: ${message}`, 'error');
      }
    },

    async onUnload(context: PluginContext): Promise<void> {
      await shutdown();
      context.ui.showNotification('Nexus Workspace Plugin unloaded', 'info');
    },

    commands: [
      {
        name: 'nexus-connect',
        description: 'Connect to Nexus Workspace',
        handler: async (context: PluginContext) => {
          if (isConnected()) {
            context.ui.showNotification('Already connected to Nexus Workspace', 'info');
            return;
          }
          await connectCommand(context);
          if (workspaceClient && config) {
            initToolIntercept(workspaceClient, config);
          }
        },
      },
      {
        name: 'nexus-status',
        description: 'Show Nexus Workspace status',
        handler: statusCommand,
      },
      {
        name: 'nexus-disconnect',
        description: 'Disconnect from Nexus Workspace',
        handler: async (context: PluginContext) => {
          await disconnectCommand(context);
        },
      },
    ],
  };
}

export default createPlugin;
