import { tool } from '@opencode-ai/plugin/tool';
import * as path from 'path';
const BOULDER_STATE_PATH = process.env.NEXUS_BOULDER_STATE_PATH ||
    path.join(process.cwd(), '.nexus', 'boulder', 'state.json');
const NEXUS_CONFIG_PATH = process.env.NEXUS_CONFIG_PATH ||
    path.join(process.cwd(), '.nexus', 'plugin-config.json');
function loadConfig(configPath) {
    const fs = require('fs');
    const path = require('path');
    const filePath = configPath || NEXUS_CONFIG_PATH;
    if (!fs.existsSync(filePath)) {
        return null;
    }
    try {
        const configContent = fs.readFileSync(filePath, 'utf-8');
        const config = JSON.parse(configContent);
        // Support both direct nexus config and nested "nexus" key
        const nexusCfg = config.nexus?.workspace?.endpoint ? config.nexus : config;
        if (!nexusCfg?.workspace?.endpoint) {
            return null;
        }
        const token = resolveEnvVariable(nexusCfg.workspace.token);
        return {
            workspace: {
                endpoint: nexusCfg.workspace.endpoint,
                workspaceId: nexusCfg.workspace.workspaceId,
                token,
            },
            options: {
                enableFileOperations: nexusCfg.options?.enableFileOperations ?? true,
                enableShellExecution: nexusCfg.options?.enableShellExecution ?? true,
                idleTimeout: nexusCfg.options?.idleTimeout ?? 300000,
                keepAliveInterval: nexusCfg.options?.keepAliveInterval ?? 60000,
                excludedPaths: nexusCfg.options?.excludedPaths ?? [],
                largeFileThreshold: nexusCfg.options?.largeFileThreshold ?? 10485760,
            },
        };
    }
    catch (error) {
        return null;
    }
}
function resolveEnvVariable(value) {
    if (!value) {
        return '';
    }
    if (value.startsWith('${') && value.endsWith('}')) {
        const envKey = value.slice(2, -1);
        return process.env[envKey] || '';
    }
    return value;
}
function validateConfig(config) {
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
    }
    catch {
        throw new Error('Invalid workspace endpoint URL');
    }
}
let nexusConfig = null;
export const nexusPlugin = async (_input) => {
    nexusConfig = loadConfig();
    if (!nexusConfig) {
        console.log('[nexus] Config not found, plugin tools will be unavailable');
    }
    else {
        try {
            validateConfig(nexusConfig);
            console.log('[nexus] Plugin loaded for workspace:', nexusConfig.workspace.workspaceId);
        }
        catch (error) {
            console.error('[nexus] Config validation failed:', error);
            nexusConfig = null;
        }
    }
    const nexusConnectTool = tool({
        description: 'Connect to Nexus Workspace',
        args: {},
        async execute(_args, _context) {
            if (!nexusConfig) {
                return 'Not configured';
            }
            return `Connected to workspace: ${nexusConfig.workspace.workspaceId} at ${nexusConfig.workspace.endpoint}`;
        },
    });
    const nexusStatusTool = tool({
        description: 'Show Nexus Workspace status',
        args: {},
        async execute(_args, _context) {
            if (!nexusConfig) {
                return 'Not configured';
            }
            return `Workspace: ${nexusConfig.workspace.workspaceId} | Endpoint: ${nexusConfig.workspace.endpoint}`;
        },
    });
    const boulderPauseTool = tool({
        description: 'Pause the boulder enforcement system',
        args: {},
        async execute(_args, _context) {
            const fs = require('fs');
            const statePath = BOULDER_STATE_PATH;
            try {
                const state = JSON.parse(fs.readFileSync(statePath, 'utf8'));
                state.status = 'PAUSED';
                state.stopRequested = true;
                fs.writeFileSync(statePath, JSON.stringify(state, null, 2));
                return '✅ Boulder paused: status=PAUSED, stopRequested=true';
            }
            catch (error) {
                return `❌ Failed to pause boulder: ${error.message}`;
            }
        },
    });
    const boulderResumeTool = tool({
        description: 'Resume the boulder enforcement system',
        args: {},
        async execute(_args, _context) {
            const fs = require('fs');
            const statePath = BOULDER_STATE_PATH;
            try {
                const state = JSON.parse(fs.readFileSync(statePath, 'utf8'));
                state.status = 'CONTINUOUS';
                state.stopRequested = false;
                fs.writeFileSync(statePath, JSON.stringify(state, null, 2));
                return '✅ Boulder resumed: status=CONTINUOUS, stopRequested=false';
            }
            catch (error) {
                return `❌ Failed to resume boulder: ${error.message}`;
            }
        },
    });
    const boulderStatusTool = tool({
        description: 'Check boulder enforcement status',
        args: {},
        async execute(_args, _context) {
            const fs = require('fs');
            const statePath = BOULDER_STATE_PATH;
            try {
                const state = JSON.parse(fs.readFileSync(statePath, 'utf8'));
                return `Status: ${state.status} | stopRequested: ${state.stopRequested} | iteration: ${state.iteration}`;
            }
            catch (error) {
                return `❌ Failed to get boulder status: ${error.message}`;
            }
        },
    });
    const hooks = {
        tool: {
            'nexus-connect': nexusConnectTool,
            'nexus-status': nexusStatusTool,
            'boulder-pause': boulderPauseTool,
            'boulder-resume': boulderResumeTool,
            'boulder-status': boulderStatusTool,
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
//# sourceMappingURL=index.js.map