import { spawn } from 'node:child_process';

async function defaultRunCommand(command, args, cwd) {
  return await new Promise((resolve) => {
    const child = spawn(command, args, {
      cwd,
      stdio: ['ignore', 'pipe', 'pipe'],
    });

    let stdout = '';
    let stderr = '';

    child.stdout.on('data', (chunk) => {
      stdout += String(chunk);
    });
    child.stderr.on('data', (chunk) => {
      stderr += String(chunk);
    });
    child.on('error', () => {
      resolve({ code: 127, stdout, stderr: stderr || 'nexus spawn error' });
    });
    child.on('exit', (code) => {
      resolve({ code: code ?? 1, stdout, stderr });
    });
  });
}

function classifyNexusFailure(stderr) {
  if (/not found|spawn/i.test(stderr || '')) {
    return 'NEXUS_UNAVAILABLE';
  }
  return 'WORKSPACE_PREP_FAILED';
}

export async function prepareNexusWorkspace(input, deps = {}) {
  const runCommand = deps.runCommand || defaultRunCommand;
  const goal = String(input?.goal || '').trim();
  const cwd = String(input?.cwd || process.cwd());
  const payload = JSON.stringify({ goal, cwd });

  const result = await runCommand('nexus', ['workspace', 'prepare', '--json', payload], cwd);

  if (result.code !== 0) {
    return {
      ok: false,
      reasonCode: classifyNexusFailure(result.stderr),
      error: result.stderr || 'Unknown nexus error',
    };
  }

  let parsed;
  try {
    parsed = JSON.parse(result.stdout || '{}');
  } catch {
    return {
      ok: false,
      reasonCode: 'WORKSPACE_PREP_FAILED',
      error: 'Nexus returned invalid JSON',
    };
  }

  return {
    ok: true,
    workspace: {
      workspacePath: parsed.workspacePath,
      workspaceId: parsed.workspaceId,
      metadata: parsed.metadata || {},
    },
  };
}
