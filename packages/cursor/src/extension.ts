import * as vscode from 'vscode';
import { exec } from 'child_process';
import { promisify } from 'util';

const execAsync = promisify(exec);

export function activate(context: vscode.ExtensionContext) {
  console.log('[NEXUS] Cursor extension activated');

  const commands = [
    vscode.commands.registerCommand('nexus.workspaceList', listWorkspaces),
    vscode.commands.registerCommand('nexus.workspaceCreate', createWorkspace),
    vscode.commands.registerCommand('nexus.workspaceUse', useWorkspace),
    vscode.commands.registerCommand('nexus.workspaceStatus', showWorkspaceStatus),
    vscode.commands.registerCommand('nexus.syncStatus', showSyncStatus),
    vscode.commands.registerCommand('nexus.workspaceOpen', openWorkspace),
    vscode.commands.registerCommand('nexus.workspaceDelete', deleteWorkspace),
  ];

  context.subscriptions.push(...commands);

  const statusBar = vscode.window.createStatusBarItem(
    vscode.StatusBarAlignment.Left,
    100
  );
  statusBar.text = '$(terminal) Nexus';
  statusBar.tooltip = 'Nexus Workspaces - Click for commands';
  statusBar.command = 'nexus.workspaceList';
  statusBar.show();
  context.subscriptions.push(statusBar);

  updateStatusBar(statusBar);
}

async function updateStatusBar(statusBar: vscode.StatusBarItem) {
  try {
    const { stdout } = await execAsync('nexus workspace list --quiet 2>/dev/null || echo ""');
    if (stdout.trim()) {
      const lines = stdout.trim().split('\n');
      if (lines.length > 0 && lines[0].includes('*')) {
        const active = lines[0].replace('*', '').trim();
        statusBar.text = `$(terminal) ${active}`;
      }
    }
  } catch {
    statusBar.text = '$(terminal) Nexus';
  }
}

async function listWorkspaces() {
  try {
    const { stdout } = await execAsync('nexus workspace list');
    const items = stdout.split('\n').filter(line => line.trim());

    if (items.length === 0) {
      vscode.window.showInformationMessage('No workspaces found');
      return;
    }

    const choices = items.map(ws => ({
      label: ws.includes('*') ? ws.replace('*', '').trim() : ws,
      description: ws.includes('*') ? '(active)' : '',
    }));

    const selected = await vscode.window.showQuickPick(choices, {
      placeHolder: 'Select a workspace',
    });

    if (selected) {
      await useWorkspace(selected.label);
    }
  } catch (error) {
    vscode.window.showErrorMessage(`Failed to list workspaces: ${error}`);
  }
}

async function createWorkspace() {
  const name = await vscode.window.showInputBox({
    prompt: 'Workspace name',
    placeHolder: 'my-workspace',
  });

  if (!name) return;

  try {
    const terminal = vscode.window.createTerminal('Nexus Create');
    terminal.sendText(`nexus workspace create ${name}`);
    terminal.show();
    vscode.window.showInformationMessage(`Creating workspace: ${name}`);
  } catch (error) {
    vscode.window.showErrorMessage(`Failed to create workspace: ${error}`);
  }
}

async function useWorkspace(name?: string) {
  const wsName = name || await vscode.window.showInputBox({
    prompt: 'Workspace name',
    placeHolder: 'workspace-name',
  });

  if (!wsName) return;

  try {
    await execAsync(`nexus workspace use ${wsName}`);
    vscode.window.showInformationMessage(`Switched to workspace: ${wsName}`);
  } catch (error) {
    vscode.window.showErrorMessage(`Failed to switch workspace: ${error}`);
  }
}

async function showWorkspaceStatus() {
  try {
    const { stdout } = await execAsync('nexus workspace status');
    vscode.window.showInformationMessage(stdout.substring(0, 200) || 'Workspace active');
  } catch (error) {
    vscode.window.showErrorMessage(`Failed to get status: ${error}`);
  }
}

async function showSyncStatus() {
  try {
    const { stdout } = await execAsync('nexus sync status');
    vscode.window.showInformationMessage(stdout.substring(0, 200) || 'Sync status: OK');
  } catch {
    vscode.window.showInformationMessage('Sync not configured');
  }
}

async function openWorkspace() {
  const name = await vscode.window.showInputBox({
    prompt: 'Workspace name to open',
    placeHolder: 'workspace-name',
  });

  if (!name) return;

  try {
    const { stdout } = await execAsync(`nexus workspace exec ${name} -- pwd`);
    const wsPath = stdout.trim();
    
    const existingFolder = vscode.workspace.workspaceFolders?.find(
      f => f.uri.fsPath === wsPath
    );
    if (!existingFolder) {
      await vscode.commands.executeCommand('vscode.openFolder', vscode.Uri.file(wsPath));
    } else {
      vscode.window.showInformationMessage('Workspace already open');
    }
  } catch (error) {
    vscode.window.showErrorMessage(`Failed to open workspace: ${error}`);
  }
}

async function deleteWorkspace() {
  const name = await vscode.window.showInputBox({
    prompt: 'Workspace name to delete',
    placeHolder: 'workspace-name',
  });

  if (!name) return;

  const confirm = await vscode.window.showWarningMessage(
    `Delete workspace "${name}"? This cannot be undone.`,
    { modal: true },
    'Delete',
    'Cancel'
  );

  if (confirm !== 'Delete') return;

  try {
    await execAsync(`nexus workspace delete ${name}`);
    vscode.window.showInformationMessage(`Deleted workspace: ${name}`);
  } catch (error) {
    vscode.window.showErrorMessage(`Failed to delete workspace: ${error}`);
  }
}

export function deactivate() {
  console.log('[NEXUS] Extension deactivated');
}
