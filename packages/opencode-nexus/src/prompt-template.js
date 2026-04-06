export function buildHandoffPrompt({ goal, sourceSessionId, workspacePath, contextSummary }) {
  const safeGoal = String(goal || '').trim() || 'Continue the current task';
  const safeSessionId = String(sourceSessionId || '').trim() || 'unknown-session';
  const safeWorkspacePath = String(workspacePath || '').trim() || '<workspace-unavailable>';
  const safeSummary = String(contextSummary || '').trim() || 'No additional summary provided.';

  return [
    `Continuing work from session ${safeSessionId}.`,
    `Workspace: ${safeWorkspacePath}`,
    '',
    'IMPORTANT: Do not modify files yet.',
    'First present your plan and wait for explicit user confirmation before making edits.',
    '',
    `Goal: ${safeGoal}`,
    '',
    'Context:',
    safeSummary,
  ].join('\n');
}
