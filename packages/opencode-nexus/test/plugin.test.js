import test from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

import { createOpencodeNexusPlugin } from '../src/plugin.js';

function textMessage(role, text, id) {
  return {
    info: { id, role },
    parts: [{ type: 'text', text }],
  };
}

function createMockContext(messagesBySession = {}, options = {}) {
  const promptCalls = [];
  const toastCalls = [];

  return {
    promptCalls,
    toastCalls,
    context: {
      directory: '/repo',
      client: {
        session: {
          async prompt(payload) {
            promptCalls.push(payload);
            return { data: { ok: true } };
          },
          async messages({ path }) {
            return { data: messagesBySession[path.id] || [] };
          },
        },
        tui: options.withTui
          ? {
              async showToast(payload) {
                toastCalls.push(payload);
              },
            }
          : undefined,
      },
    },
  };
}

test('package and root opencode-nexus entrypoints export plugin factory', async () => {
  const pkg = await import('../index.js');
  const rootEntry = pathToFileURL(
    path.join(process.cwd(), '.opencode/plugins/opencode-nexus.js'),
  ).href;
  const root = await import(rootEntry);

  assert.equal(typeof pkg.createOpencodeNexusPlugin, 'function');
  assert.equal(typeof createOpencodeNexusPlugin, 'function');
  assert.equal(typeof root.OpencodeNexusPlugin, 'function');
});

test('/handoff uses nexus prepare and writes transfer prompt with confirmation gate', async () => {
  let transferPayload;
  const { context } = createMockContext();

  const pluginFactory = createOpencodeNexusPlugin({
    prepareWorkspace: async () => ({
      ok: true,
      workspace: {
        workspacePath: '/tmp/ws',
        workspaceId: 'ws-1',
        metadata: {},
      },
    }),
    transferSession: async (payload) => {
      transferPayload = payload;
      return { mode: 'mock' };
    },
  });

  const plugin = await pluginFactory(context);
  const result = await plugin.tool.handoff_session.execute(
    {
      goal: 'Implement parser',
      contextSummary: 'Parser branch and failing tests are ready.',
    },
    { sessionID: 'sess_abc' },
  );

  assert.equal(transferPayload.workspace.workspacePath, '/tmp/ws');
  assert.match(transferPayload.prompt, /wait for explicit user confirmation before making edits/i);
  assert.match(transferPayload.prompt, /Continuing work from session sess_abc/);
  assert.match(result, /Handoff ready for workspace \/tmp\/ws/);
});

test('handoff_session falls back to draft-only when workspace prepare fails', async () => {
  const { context } = createMockContext();
  const pluginFactory = createOpencodeNexusPlugin({
    prepareWorkspace: async () => ({
      ok: false,
      reasonCode: 'NEXUS_UNAVAILABLE',
      error: 'nexus: command not found',
    }),
  });

  const plugin = await pluginFactory(context);
  const result = await plugin.tool.handoff_session.execute(
    {
      goal: 'Implement parser',
      contextSummary: 'Parser branch and failing tests are ready.',
    },
    { sessionID: 'sess_abc' },
  );

  assert.match(result, /Handoff draft created without workspace \(NEXUS_UNAVAILABLE\)/);
  assert.match(result, /wait for explicit user confirmation before making edits/i);
});

test('handoff_session returns draft text when transfer runs in draft-only mode', async () => {
  const { context } = createMockContext();

  const pluginFactory = createOpencodeNexusPlugin({
    prepareWorkspace: async () => ({
      ok: true,
      workspace: {
        workspacePath: '/tmp/ws',
        workspaceId: 'ws-1',
        metadata: {},
      },
    }),
    transferSession: async ({ prompt }) => ({
      mode: 'draft_only',
      prompt,
    }),
  });

  const plugin = await pluginFactory(context);
  const result = await plugin.tool.handoff_session.execute(
    {
      goal: 'Implement parser',
      contextSummary: 'Parser branch and failing tests are ready.',
    },
    { sessionID: 'sess_abc' },
  );

  assert.match(result, /Handoff draft created in current session/);
  assert.match(result, /wait for explicit user confirmation before making edits/i);
});

test('plugin config registers /handoff command template', async () => {
  const { context } = createMockContext();
  const pluginFactory = createOpencodeNexusPlugin();
  const plugin = await pluginFactory(context);
  const mutableConfig = {};

  await plugin.config(mutableConfig);

  assert.equal(typeof mutableConfig.command.handoff.description, 'string');
  assert.equal(typeof mutableConfig.command.handoff.template, 'string');
  assert.match(mutableConfig.command.handoff.template, /handoff_session/);
});

test('read_session returns formatted transcript', async () => {
  const sessionId = 'sess-read-1';
  const { context } = createMockContext({
    [sessionId]: [
      textMessage('user', 'Plan this feature.', 'u1'),
      textMessage('assistant', 'Here is the implementation plan.', 'a1'),
    ],
  });

  const pluginFactory = createOpencodeNexusPlugin();
  const plugin = await pluginFactory(context);

  const output = await plugin.tool.read_session.execute({ sessionID: sessionId, limit: 50 });

  assert.match(output, /## User/);
  assert.match(output, /Plan this feature/);
  assert.match(output, /## Assistant/);
  assert.match(output, /implementation plan/);
});

test('declined suggestion is not retried in same session', async () => {
  const sessionId = 'session-1';
  const messagesBySession = {
    [sessionId]: [
      textMessage('user', 'Keep moving.', 'user-1'),
      textMessage('assistant', 'I can continue in a new workspace if helpful.', 'assistant-1'),
    ],
  };
  const { context, promptCalls } = createMockContext(messagesBySession);
  const pluginFactory = createOpencodeNexusPlugin();
  const plugin = await pluginFactory(context);

  await plugin.event({
    event: {
      type: 'session.idle',
      properties: { sessionID: sessionId },
    },
  });

  messagesBySession[sessionId] = [
    ...messagesBySession[sessionId],
    textMessage('user', 'No handoff for now; keep this session.', 'user-2'),
  ];

  await plugin.event({
    event: {
      type: 'session.updated',
      properties: { sessionID: sessionId },
    },
  });

  messagesBySession[sessionId] = [
    ...messagesBySession[sessionId],
    textMessage('assistant', 'I can continue in a new workspace if helpful.', 'assistant-2'),
  ];

  await plugin.event({
    event: {
      type: 'session.idle',
      properties: { sessionID: sessionId },
    },
  });

  assert.equal(promptCalls.length, 1);
});

test('session.idle supports top-level sessionID event shape', async () => {
  const sessionId = 'session-top-level-id';
  const { context, promptCalls } = createMockContext({
    [sessionId]: [
      textMessage('user', 'Keep going.', 'user-1'),
      textMessage('assistant', 'I can continue in a new workspace if helpful.', 'assistant-1'),
    ],
  });
  const pluginFactory = createOpencodeNexusPlugin();
  const plugin = await pluginFactory(context);

  await plugin.event({
    event: {
      type: 'session.idle',
      sessionID: sessionId,
    },
  });

  assert.equal(promptCalls.length, 1);
});

test('session.idle uses toast suggestion channel when tui is available', async () => {
  const sessionId = 'session-toast-id';
  const { context, promptCalls, toastCalls } = createMockContext(
    {
      [sessionId]: [
        textMessage('user', 'Keep moving.', 'user-1'),
        textMessage('assistant', 'I can continue in a new workspace if helpful.', 'assistant-1'),
      ],
    },
    { withTui: true },
  );

  const pluginFactory = createOpencodeNexusPlugin();
  const plugin = await pluginFactory(context);

  await plugin.event({
    event: {
      type: 'session.idle',
      properties: { sessionID: sessionId },
    },
  });

  assert.equal(promptCalls.length, 0);
  assert.equal(toastCalls.length, 1);
  assert.match(toastCalls[0].body.message, /\/handoff <goal>/);
});

test('event handling fails open when session APIs throw', async () => {
  const pluginFactory = createOpencodeNexusPlugin();
  const plugin = await pluginFactory({
    directory: '/repo',
    client: {
      session: {
        async messages() {
          throw new Error('messages unavailable');
        },
        async prompt() {
          throw new Error('prompt unavailable');
        },
      },
    },
  });

  await assert.doesNotReject(async () => {
    await plugin.event({
      event: {
        type: 'session.updated',
        sessionID: 'session-error-path',
      },
    });
  });
});
