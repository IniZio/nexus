import { tool } from '@opencode-ai/plugin/tool';
import type { Plugin, Hooks } from '@opencode-ai/plugin';

interface NexusConfig {
  workspace: {
    endpoint: string;
    workspaceId: string;
    token: string;
  };
  options?: {
    enableFileOperations?: boolean;
    enableShellExecution?: boolean;
    idleTimeout?: number;
    keepAliveInterval?: number;
    excludedPaths?: string[];
    largeFileThreshold?: number;
  };
}

interface OpenCodeConfig {
  plugin?: string[];
  nexus?: NexusConfig;
}

function loadConfig(configPath?: string): NexusConfig {
  const fs = require('fs');
  const path = require('path');
  
  const defaultPath = path.join(process.cwd(), 'opencode.json');
  const filePath = configPath || process.env.OPENCODE_CONFIG_PATH || defaultPath;

  if (!fs.existsSync(filePath)) {
    throw new Error(`Configuration file not found: ${filePath}`);
  }

  const configContent = fs.readFileSync(filePath, 'utf-8');
  const config: OpenCodeConfig = JSON.parse(configContent);

  if (!config.nexus?.workspace?.endpoint) {
    throw new Error('Nexus workspace configuration is required');
  }

  const token = resolveEnvVariable(config.nexus.workspace.token);

  return {
    workspace: {
      endpoint: config.nexus.workspace.endpoint,
      workspaceId: config.nexus.workspace.workspaceId,
      token,
    },
    options: {
      enableFileOperations: config.nexus.options?.enableFileOperations ?? true,
      enableShellExecution: config.nexus.options?.enableShellExecution ?? true,
      idleTimeout: config.nexus.options?.idleTimeout ?? 300000,
      keepAliveInterval: config.nexus.options?.keepAliveInterval ?? 60000,
      excludedPaths: config.nexus.options?.excludedPaths ?? [],
      largeFileThreshold: config.nexus.options?.largeFileThreshold ?? 10485760,
    },
  };
}

function resolveEnvVariable(value: string | undefined): string {
  if (!value) {
    return '';
  }
  if (value.startsWith('${') && value.endsWith('}')) {
    const envKey = value.slice(2, -1);
    return process.env[envKey] || '';
  }
  return value;
}

function validateConfig(config: NexusConfig): void {
  if (!config.workspace.endpoint) {
    throw new Error('Workspace endpoint is required');
  }
  if (!config.workspace.workspaceId) {
    throw new Error('Workspace ID is required');
  }
  if (!config.workspace.token) {
    throw new Error('Workspace token is required');
  }

  try {
    new URL(config.workspace.endpoint);
  } catch {
    throw new Error('Invalid workspace endpoint URL');
  }
}

let nexusConfig: NexusConfig | null = null;

export const nexusPlugin: Plugin = async (_input) => {
  try {
    nexusConfig = loadConfig();
    validateConfig(nexusConfig);

    console.log('[nexus] Plugin loaded for workspace:', nexusConfig.workspace.workspaceId);
  } catch (error) {
    console.error('[nexus] Failed to load config:', error);
  }

  const nexusConnectTool = tool({
    description: 'Connect to Nexus Workspace',
    args: {},
    async execute(_args, _context) {
      if (!nexusConfig) {
        return 'Configuration not loaded';
      }
      return `Connected to workspace: ${nexusConfig.workspace.workspaceId} at ${nexusConfig.workspace.endpoint}`;
    },
  });

  const nexusStatusTool = tool({
    description: 'Show Nexus Workspace status',
    args: {},
    async execute(_args, _context) {
      if (!nexusConfig) {
        return 'Not connected to Nexus Workspace';
      }
      return `Workspace: ${nexusConfig.workspace.workspaceId} | Endpoint: ${nexusConfig.workspace.endpoint}`;
    },
  });

  const hooks: Hooks = {
    tool: {
      'nexus-connect': nexusConnectTool,
      'nexus-status': nexusStatusTool,
    },
    
    'tool.execute.before': async ({ tool: toolName }) => {
      if (!nexusConfig) {
        return;
      }

      console.log('[nexus] Tool executed:', toolName);
    },
    
    'tool.execute.after': async ({ tool: toolName }) => {
      if (!nexusConfig) {
        return;
      }

      console.log('[nexus] Tool completed:', toolName);
    },
  };

  return hooks;
};

export default nexusPlugin;
