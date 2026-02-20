import { Tool } from "@modelcontextprotocol/sdk/types.js";

export interface CreateSessionInput {
  directory?: string;
  agent?: string;
  model?: string;
}

export interface SessionOutput {
  session: {
    id: string;
    directory?: string;
    agent?: string;
    model?: string;
    created: string;
  };
}

function generateSessionId(): string {
  return `session_${Date.now()}_${Math.random().toString(36).slice(2, 9)}`;
}

export function createSessionTool(): Tool {
  return {
    name: "create_session",
    description: "Create a new OpenCode session",
    inputSchema: {
      type: "object",
      properties: {
        directory: {
          type: "string",
          description: "Working directory for the session",
        },
        agent: {
          type: "string",
          description: "Agent type to use (e.g., 'executor', 'explorer')",
        },
        model: {
          type: "string",
          description: "Model to use (e.g., 'haiku', 'sonnet', 'opus')",
        },
      },
      additionalProperties: false,
    },
    outputSchema: {
      type: "object",
      properties: {
        session: {
          type: "object",
          properties: {
            id: { type: "string" },
            directory: { type: "string" },
            agent: { type: "string" },
            model: { type: "string" },
            created: { type: "string" },
          },
          required: ["id", "created"],
          additionalProperties: false,
        },
      },
      required: ["session"],
      additionalProperties: false,
    },
  };
}

export async function handleCreateSession(
  input: CreateSessionInput
): Promise<SessionOutput> {
  const sessionId = generateSessionId();
  const timestamp = new Date().toISOString();

  return {
    session: {
      id: sessionId,
      directory: input.directory,
      agent: input.agent,
      model: input.model,
      created: timestamp,
    },
  };
}
