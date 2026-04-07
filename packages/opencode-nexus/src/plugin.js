import { runHandoff } from './handoff-flow.js';

const HANDOFF_COMMAND_TEMPLATE = [
  'Prepare a Nexus workspace and hand off this task using the handoff_session tool.',
  'Summarize only the context needed to continue work quickly.',
  'Then call: handoff_session(goal="$ARGUMENTS", contextSummary="...")',
  'The transferred prompt must wait for explicit user confirmation before edits.',
].join(' ');

const DEFAULT_OPTIONS = {
  suggestionMode: 'semi_auto',
  allowExplicitCommandFallback: true,
  confirmationRequired: true,
};

function normalizeText(value) {
  return String(value ?? '')
    .replace(/\s+/g, ' ')
    .trim();
}

function extractTextFromParts(parts = []) {
  return normalizeText(
    parts
      .flatMap((part) => {
        if (!part || typeof part !== 'object') return [];
        if (typeof part.text === 'string') return [part.text];
        if (typeof part.content === 'string') return [part.content];
        return [];
      })
      .join('\n'),
  );
}

function findLastByRole(messages, role) {
  if (!Array.isArray(messages)) return null;
  for (let index = messages.length - 1; index >= 0; index -= 1) {
    const message = messages[index];
    if (message?.info?.role === role) return message;
  }
  return null;
}

function isDeclineText(text) {
  return /\b(no handoff|dont handoff|don't handoff|do not handoff|stay here|keep this session)\b/i.test(text);
}

function isHandoffCandidate(assistantText) {
  return /\b(handoff|new session|workspace|continue in|continue from)\b/i.test(assistantText);
}

function resolveSessionIdFromEvent(event) {
  return (
    event?.properties?.info?.id ??
    event?.properties?.sessionID ??
    event?.properties?.sessionId ??
    event?.info?.id ??
    event?.sessionID ??
    event?.sessionId ??
    null
  );
}

function formatTranscript(messages = [], limit = 100) {
  const lines = [];

  for (const message of messages) {
    if (message?.info?.role === 'user') {
      lines.push('## User');
      lines.push(extractTextFromParts(message.parts));
      lines.push('');
      continue;
    }

    if (message?.info?.role === 'assistant') {
      lines.push('## Assistant');
      lines.push(extractTextFromParts(message.parts));
      lines.push('');
      continue;
    }
  }

  const text = lines.join('\n').trim();
  if (messages.length >= limit) {
    return `${text}\n\n(Showing ${messages.length} most recent messages. Use a higher limit to see more.)`;
  }

  return `${text}\n\n(End of session - ${messages.length} messages)`;
}

async function wait(ms) {
  await new Promise((resolve) => {
    setTimeout(resolve, ms);
  });
}

function createDefaultTransferSession(client) {
  return async ({ prompt }) => {
    if (client?.tui?.executeCommand && client?.tui?.appendPrompt) {
      await client.tui.executeCommand({ body: { command: 'session_new' } });
      await wait(150);
      await client.tui.appendPrompt({ body: { text: prompt } });
      if (client?.tui?.showToast) {
        await client.tui.showToast({
          body: {
            title: 'Handoff Ready',
            message: 'Review the draft and confirm before making edits.',
            variant: 'success',
            duration: 4000,
          },
        });
      }
      return { mode: 'tui_session_new' };
    }

    return {
      mode: 'draft_only',
      prompt,
    };
  };
}

export function createOpencodeNexusPlugin(options = {}) {
  const config = {
    ...DEFAULT_OPTIONS,
    ...(options || {}),
  };

  return async (ctx) => {
    const declinedBySession = new Set();
    const suggestedBySession = new Set();
    const transferSession = config.transferSession || createDefaultTransferSession(ctx.client);

    async function readMessages(sessionId) {
      if (!sessionId || !ctx?.client?.session?.messages) return [];
      try {
        const response = await ctx.client.session.messages({ path: { id: sessionId } });
        return Array.isArray(response?.data) ? response.data : [];
      } catch (error) {
        if (ctx?.client?.tui?.showToast) {
          await ctx.client.tui.showToast({
            body: {
              title: 'Nexus Handoff Warning',
              message: `Could not read session ${sessionId}: ${String(error?.message || error)}`,
              variant: 'warning',
              duration: 3500,
            },
          });
        }
        return [];
      }
    }

    async function maybeTrackDecline(sessionId) {
      if (!sessionId) return false;
      const messages = await readMessages(sessionId);
      const lastUser = findLastByRole(messages, 'user');
      const userText = extractTextFromParts(lastUser?.parts);
      if (!userText) return false;
      if (!isDeclineText(userText)) return false;
      declinedBySession.add(sessionId);
      return true;
    }

    async function maybeSuggest(sessionId) {
      if (!sessionId) return false;
      if (config.suggestionMode !== 'semi_auto') return false;
      if (declinedBySession.has(sessionId)) return false;
      if (suggestedBySession.has(sessionId)) return false;

      const messages = await readMessages(sessionId);
      const lastAssistant = findLastByRole(messages, 'assistant');
      const assistantText = extractTextFromParts(lastAssistant?.parts);
      if (!isHandoffCandidate(assistantText)) return false;

      const suggestionText =
        'I can prepare a Nexus workspace and transfer this task. Run `/handoff <goal>` if you want me to hand off now.';

      if (ctx?.client?.tui?.showToast) {
        try {
          await ctx.client.tui.showToast({
            body: {
              title: 'Nexus Handoff Available',
              message: suggestionText,
              variant: 'info',
              duration: 4000,
            },
          });
        } catch {
          return false;
        }

        suggestedBySession.add(sessionId);
        return true;
      }

      if (!ctx?.client?.session?.prompt) return false;
      try {
        await ctx.client.session.prompt({
          path: { id: sessionId },
          body: {
            noReply: true,
            parts: [
              {
                type: 'text',
                text: suggestionText,
              },
            ],
          },
        });
      } catch {
        return false;
      }

      suggestedBySession.add(sessionId);
      return true;
    }

    return {
      config: async (mutableConfig) => {
        mutableConfig.command = mutableConfig.command || {};
        mutableConfig.command.handoff = {
          description: 'Prepare a Nexus workspace and hand off work to a new OpenCode session',
          template: HANDOFF_COMMAND_TEMPLATE,
        };
      },
      tool: {
        handoff_session: {
          description: 'Prepare Nexus workspace and create handoff draft for a new OpenCode session',
          args: {
            goal: {
              type: 'string',
            },
            contextSummary: {
              type: 'string',
              optional: true,
            },
          },
          execute: async (args, toolContext) => {
            const goal = normalizeText(args?.goal);
            const contextSummary = normalizeText(args?.contextSummary);
            const sessionId =
              toolContext?.sessionID || toolContext?.sessionId || toolContext?.session?.id || 'unknown-session';

            const result = await runHandoff(
              {
                goal,
                sessionId,
                cwd: ctx.directory,
                contextSummary,
              },
              {
                prepareWorkspace: config.prepareWorkspace,
                runCommand: config.runCommand,
                transferSession,
              },
            );

            if (!result.ok) {
              return [
                `Handoff draft created without workspace (${result.reasonCode}).`,
                '',
                result.draftOnly,
              ].join('\n');
            }

            if (result?.transfer?.mode === 'draft_only') {
              return [
                `Handoff draft created in current session for workspace ${result.workspace.workspacePath}.`,
                '',
                result.transfer.prompt || result.prompt,
              ].join('\n');
            }

            return `Handoff ready for workspace ${result.workspace.workspacePath}. Review the draft and confirm before making edits.`;
          },
        },
        read_session: {
          description:
            'Read transcript from a previous session when specific details are missing from handoff summary',
          args: {
            sessionID: {
              type: 'string',
            },
            limit: {
              type: 'number',
              optional: true,
            },
          },
          execute: async (args) => {
            if (!ctx?.client?.session?.messages) {
              return 'Session transcript API is unavailable in this environment.';
            }

            const sessionID = normalizeText(args?.sessionID);
            const limit = Math.min(Number(args?.limit || 100), 500);

            try {
              const response = await ctx.client.session.messages({
                path: { id: sessionID },
                query: { limit },
              });
              const messages = Array.isArray(response?.data) ? response.data : [];

              if (!messages.length) {
                return 'Session has no messages or does not exist.';
              }

              return formatTranscript(messages, limit);
            } catch (error) {
              return `Could not read session ${sessionID}: ${String(error?.message || error)}`;
            }
          },
        },
      },
      event: async ({ event }) => {
        if (!event?.type) return;
        const sessionId = resolveSessionIdFromEvent(event);

        try {
          if (event.type === 'session.deleted' && sessionId) {
            declinedBySession.delete(sessionId);
            suggestedBySession.delete(sessionId);
            return;
          }

          if (event.type === 'session.updated') {
            await maybeTrackDecline(sessionId);
            return;
          }

          if (event.type === 'session.idle') {
            await maybeTrackDecline(sessionId);
            await maybeSuggest(sessionId);
          }
        } catch (error) {
          if (ctx?.client?.tui?.showToast) {
            await ctx.client.tui.showToast({
              body: {
                title: 'Nexus Handoff Warning',
                message: `Event handling skipped: ${String(error?.message || error)}`,
                variant: 'warning',
                duration: 3500,
              },
            });
          }
        }
      },
      _debug: {
        declinedBySession,
        suggestedBySession,
        maybeTrackDecline,
        maybeSuggest,
      },
    };
  };
}

export const createNexusHandoffPlugin = createOpencodeNexusPlugin;
