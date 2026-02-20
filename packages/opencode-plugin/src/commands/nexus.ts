import { PluginContext } from '@opencode-ai/sdk';
import { WorkspaceClient } from '@nexus/workspace-sdk';
import { loadConfig, validateConfig } from '../config';
import { initActivityTracking, stopActivityTracking, getActivityStatus } from '../hooks/activity';

let workspaceClient: WorkspaceClient | null = null;

export async function connectCommand(context: PluginContext): Promise<void> {
  try {
    const config = loadConfig();
    validateConfig(config);

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

    context.ui.showNotification('Connected to Nexus Workspace', 'success');
  } catch (error) {
    const message = error instanceof Error ? error.message : 'Unknown error';
    context.ui.showNotification(`Failed to connect: ${message}`, 'error');
    throw error;
  }
}

export async function statusCommand(context: PluginContext): Promise<void> {
  if (!workspaceClient) {
    context.ui.showNotification('Not connected to Nexus Workspace', 'info');
    return;
  }

  try {
    const status = await workspaceClient.getStatus();
    const activity = getActivityStatus();

    const statusInfo = {
      connected: true,
      workspaceId: workspaceClient.workspaceId,
      status: status.state,
      lastActivity: new Date(activity.lastActivity).toISOString(),
      isActive: activity.isActive,
    };

    context.ui.showNotification(
      `Workspace: ${statusInfo.workspaceId} | Status: ${statusInfo.status}`,
      'info'
    );
  } catch (error) {
    context.ui.showNotification('Failed to get workspace status', 'error');
  }
}

export async function disconnectCommand(context: PluginContext): Promise<void> {
  try {
    stopActivityTracking();

    if (workspaceClient) {
      await workspaceClient.setStatus('disconnected');
      await workspaceClient.disconnect();
      workspaceClient = null;
    }

    context.ui.showNotification('Disconnected from Nexus Workspace', 'success');
  } catch (error) {
    const message = error instanceof Error ? error.message : 'Unknown error';
    context.ui.showNotification(`Failed to disconnect: ${message}`, 'error');
    throw error;
  }
}

export function getWorkspaceClient(): WorkspaceClient | null {
  return workspaceClient;
}

export function isConnected(): boolean {
  return workspaceClient !== null;
}
