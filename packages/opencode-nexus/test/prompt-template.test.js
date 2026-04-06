import test from 'node:test';
import assert from 'node:assert/strict';

import { buildHandoffPrompt } from '../src/prompt-template.js';

test('buildHandoffPrompt includes mandatory confirmation gate', () => {
  const text = buildHandoffPrompt({
    goal: 'Implement parser',
    sourceSessionId: 'sess_abc',
    workspacePath: '/tmp/ws',
    contextSummary: 'Parser branch and failing tests are ready.',
  });

  assert.match(text, /wait for explicit user confirmation before making edits/i);
  assert.match(text, /Continuing work from session sess_abc/);
  assert.match(text, /Workspace: \/tmp\/ws/);
});
