import type { PluginContext, ToolHookData } from '../types/opencode-sdk';
import { WorkspaceClient } from '../types/workspace-sdk';
import * as path from 'path';
import * as fs from 'fs';
import { loadConfig, validateConfig } from '../config';
import { recordActivity } from './activity';

let workspaceClient: WorkspaceClient | null = null;
let config: ReturnType<typeof loadConfig> | null = null;

export function initToolIntercept(client: WorkspaceClient, cfg: ReturnType<typeof loadConfig>): void {
  workspaceClient = client;
  config = cfg;
}

export function registerToolHooks(context: PluginContext): void {
  context.hooks.on('tool.execute.before', async (data: ToolHookData) => {
    if (!workspaceClient || !config) {
      return;
    }

    recordActivity();

    const toolName = data.tool.name;

    if (toolName === 'read') {
      await interceptRead(data);
    } else if (toolName === 'write') {
      await interceptWrite(data);
    } else if (toolName === 'edit') {
      await interceptEdit(data);
    } else if (toolName === 'bash' && config.options?.enableShellExecution) {
      await interceptBash(data);
    }
  });
}

async function interceptRead(data: ToolHookData): Promise<void> {
  const args = data.tool.arguments as { filePath: string };
  if (!args?.filePath) {
    return;
  }

  if (shouldUseLocal(args.filePath)) {
    return;
  }

  try {
    const content = await workspaceClient!.fs.readFile(args.filePath);
    data.tool.result = content;
    data.tool.preventExecution = true;
  } catch (error) {
    console.error(`[nexus] Failed to read remote file ${args.filePath}:`, error);
  }
}

async function interceptWrite(data: ToolHookData): Promise<void> {
  const args = data.tool.arguments as { filePath: string; content: string };
  if (!args?.filePath || args.content === undefined) {
    return;
  }

  if (shouldUseLocal(args.filePath)) {
    return;
  }

  try {
    await workspaceClient!.fs.writeFile(args.filePath, args.content);
    data.tool.result = { success: true };
    data.tool.preventExecution = true;
  } catch (error) {
    console.error(`[nexus] Failed to write remote file ${args.filePath}:`, error);
  }
}

async function interceptEdit(data: ToolHookData): Promise<void> {
  const args = data.tool.arguments as { filePath: string; oldString: string; newString: string };
  if (!args?.filePath || !args.oldString || args.newString === undefined) {
    return;
  }

  if (shouldUseLocal(args.filePath)) {
    return;
  }

  try {
    const currentContent = await workspaceClient!.fs.readFile(args.filePath);
    const newContent = currentContent.replace(args.oldString, args.newString);
    await workspaceClient!.fs.writeFile(args.filePath, newContent);
    data.tool.result = { success: true };
    data.tool.preventExecution = true;
  } catch (error) {
    console.error(`[nexus] Failed to edit remote file ${args.filePath}:`, error);
  }
}

async function interceptBash(data: ToolHookData): Promise<void> {
  const args = data.tool.arguments as { command: string };
  if (!args?.command) {
    return;
  }

  if (shouldUseLocal(args.command)) {
    return;
  }

  try {
    const result = await workspaceClient!.exec(args.command, {
      timeout: 120000,
    });
    data.tool.result = result;
    data.tool.preventExecution = true;
  } catch (error) {
    console.error(`[nexus] Failed to execute remote command:`, error);
  }
}

function shouldUseLocal(filePath: string): boolean {
  if (!config) {
    return true;
  }

  if (config.options?.excludedPaths) {
    for (const excluded of config.options.excludedPaths) {
      if (filePath.startsWith(excluded)) {
        return true;
      }
    }
  }

  if (filePath.startsWith('/home/newman/magic/nexus/.claude') || filePath.startsWith('/home/newman/magic/nexus/.omc')) {
    return true;
  }

  try {
    const stats = fs.statSync(filePath);
    if (config.options?.largeFileThreshold && stats.size > config.options.largeFileThreshold) {
      return true;
    }
  } catch {
  }

  return false;
}
