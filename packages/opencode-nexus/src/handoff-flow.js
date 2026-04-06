import { prepareNexusWorkspace } from './nexus-prepare.js';
import { buildHandoffPrompt } from './prompt-template.js';

export async function runHandoff({ goal, sessionId, cwd, contextSummary }, deps = {}) {
  const prepareWorkspace = deps.prepareWorkspace || prepareNexusWorkspace;
  const transferSession =
    deps.transferSession ||
    (async ({ prompt }) => ({
      mode: 'draft_only',
      prompt,
    }));

  const prep = await prepareWorkspace({ goal, cwd }, deps);
  if (!prep.ok) {
    const draftOnly = buildHandoffPrompt({
      goal,
      sourceSessionId: sessionId,
      workspacePath: '<workspace-unavailable>',
      contextSummary,
    });
    return {
      ok: false,
      reasonCode: prep.reasonCode,
      error: prep.error,
      draftOnly,
    };
  }

  const prompt = buildHandoffPrompt({
    goal,
    sourceSessionId: sessionId,
    workspacePath: prep.workspace.workspacePath,
    contextSummary,
  });

  const transfer = await transferSession({ prompt, workspace: prep.workspace });

  return {
    ok: true,
    workspace: prep.workspace,
    prompt,
    transfer,
  };
}
