import { Tool } from "@modelcontextprotocol/sdk/types.js";

export interface SendMessageInput {
  sessionId: string;
  content: string;
  waitForResponse?: boolean;
}

export interface GetMessagesInput {
  sessionId: string;
  limit?: number;
  since?: number;
}

export interface WaitForMessageInput {
  sessionId: string;
  pattern: string;
  timeout?: number;
}

export interface Message {
  id: string;
  sessionId: string;
  content: string;
  timestamp: string;
  role: "user" | "assistant";
}

export interface SendMessageOutput {
  message: Message;
}

export interface GetMessagesOutput {
  messages: Message[];
}

export interface WaitForMessageOutput {
  message: Message;
}

const inMemoryMessages: Map<string, Message[]> = new Map();

function generateMessageId(): string {
  return `msg_${Date.now()}_${Math.random().toString(36).slice(2, 9)}`;
}

export function createSendMessageTool(): Tool {
  return {
    name: "send_message",
    description: "Send a message to an OpenCode session",
    inputSchema: {
      type: "object",
      properties: {
        sessionId: {
          type: "string",
          description: "The session ID to send the message to",
        },
        content: {
          type: "string",
          description: "The message content to send",
        },
        waitForResponse: {
          type: "boolean",
          description: "Whether to wait for a response (default: true)",
        },
      },
      required: ["sessionId", "content"],
      additionalProperties: false,
    },
    outputSchema: {
      type: "object",
      properties: {
        message: {
          type: "object",
          properties: {
            id: { type: "string" },
            sessionId: { type: "string" },
            content: { type: "string" },
            timestamp: { type: "string" },
            role: { type: "string", enum: ["user", "assistant"] },
          },
          required: ["id", "sessionId", "content", "timestamp", "role"],
          additionalProperties: false,
        },
      },
      required: ["message"],
      additionalProperties: false,
    },
  };
}

export function createGetMessagesTool(): Tool {
  return {
    name: "get_messages",
    description: "Retrieve messages from an OpenCode session",
    inputSchema: {
      type: "object",
      properties: {
        sessionId: {
          type: "string",
          description: "The session ID to retrieve messages from",
        },
        limit: {
          type: "number",
          description: "Maximum number of messages to return (default: 50)",
        },
        since: {
          type: "number",
          description: "Only return messages after this timestamp (Unix ms)",
        },
      },
      required: ["sessionId"],
      additionalProperties: false,
    },
    outputSchema: {
      type: "object",
      properties: {
        messages: {
          type: "array",
          items: {
            type: "object",
            properties: {
              id: { type: "string" },
              sessionId: { type: "string" },
              content: { type: "string" },
              timestamp: { type: "string" },
              role: { type: "string", enum: ["user", "assistant"] },
            },
            required: ["id", "sessionId", "content", "timestamp", "role"],
            additionalProperties: false,
          },
        },
      },
      required: ["messages"],
      additionalProperties: false,
    },
  };
}

export function createWaitForMessageTool(): Tool {
  return {
    name: "wait_for_message",
    description: "Wait for a message matching a pattern in an OpenCode session",
    inputSchema: {
      type: "object",
      properties: {
        sessionId: {
          type: "string",
          description: "The session ID to wait for messages from",
        },
        pattern: {
          type: "string",
          description: "Regex pattern to match against message content",
        },
        timeout: {
          type: "number",
          description: "Timeout in milliseconds (default: 30000)",
        },
      },
      required: ["sessionId", "pattern"],
      additionalProperties: false,
    },
    outputSchema: {
      type: "object",
      properties: {
        message: {
          type: "object",
          properties: {
            id: { type: "string" },
            sessionId: { type: "string" },
            content: { type: "string" },
            timestamp: { type: "string" },
            role: { type: "string", enum: ["user", "assistant"] },
          },
          required: ["id", "sessionId", "content", "timestamp", "role"],
          additionalProperties: false,
        },
      },
      required: ["message"],
      additionalProperties: false,
    },
  };
}

export async function handleSendMessage(
  input: SendMessageInput
): Promise<SendMessageOutput> {
  const timestamp = new Date().toISOString();
  const message: Message = {
    id: generateMessageId(),
    sessionId: input.sessionId,
    content: input.content,
    timestamp,
    role: "user",
  };

  const sessionMessages = inMemoryMessages.get(input.sessionId) || [];
  sessionMessages.push(message);
  inMemoryMessages.set(input.sessionId, sessionMessages);

  return { message };
}

export async function handleGetMessages(
  input: GetMessagesInput
): Promise<GetMessagesOutput> {
  const sessionMessages = inMemoryMessages.get(input.sessionId) || [];
  let filtered = sessionMessages;

  if (input.since !== undefined) {
    filtered = sessionMessages.filter((m) => new Date(m.timestamp).getTime() > input.since!);
  }

  const limit = input.limit || 50;
  const messages = filtered.slice(-limit);

  return { messages };
}

export async function handleWaitForMessage(
  input: WaitForMessageInput
): Promise<WaitForMessageOutput> {
  const timeout = input.timeout || 30000;
  const startTime = Date.now();
  const pattern = new RegExp(input.pattern);
  const pollInterval = 500;

  while (Date.now() - startTime < timeout) {
    const sessionMessages = inMemoryMessages.get(input.sessionId) || [];
    const recentMessages = sessionMessages.slice(-10);

    for (const msg of recentMessages) {
      if (pattern.test(msg.content)) {
        return { message: msg };
      }
    }

    await new Promise((resolve) => setTimeout(resolve, pollInterval));
  }

  throw new Error(`Timeout waiting for message matching pattern: ${input.pattern}`);
}
