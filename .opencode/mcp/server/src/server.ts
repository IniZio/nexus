import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import {
  CallToolRequestSchema,
  ListToolsRequestSchema,
  TextContentSchema,
} from "@modelcontextprotocol/sdk/types.js";
import {
  createSessionTool,
  handleCreateSession,
} from "./tools/session.js";
import {
  createSendMessageTool,
  createGetMessagesTool,
  createWaitForMessageTool,
  handleSendMessage,
  handleGetMessages,
  handleWaitForMessage,
} from "./tools/message.js";
import {
  createGetToastsTool,
  createWaitForToastTool,
  createTakeScreenshotTool,
  createGetTuiStateTool,
  handleGetToasts,
  handleWaitForToast,
  handleTakeScreenshot,
  handleGetTuiState,
} from "./tools/ui.js";

function toContent<T>(data: T) {
  return {
    content: [
      {
        type: "text",
        text: JSON.stringify(data, null, 2),
      } as { type: "text"; text: string },
    ],
  };
}

export function createServer(): Server {
  const server = new Server(
    {
      name: "@nexus/opencode-mcp-server",
      version: "0.1.0",
    },
    {
      capabilities: {
        tools: {},
      }
    }
  );

  server.setRequestHandler(ListToolsRequestSchema, async () => {
    return {
      tools: [
        createSessionTool(),
        createSendMessageTool(),
        createGetMessagesTool(),
        createWaitForMessageTool(),
        createGetToastsTool(),
        createWaitForToastTool(),
        createTakeScreenshotTool(),
        createGetTuiStateTool(),
      ],
    };
  });

  server.setRequestHandler(CallToolRequestSchema, async (request) => {
    const name = request.params.name;
    const args = request.params.arguments as Record<string, unknown>;
    switch (name) {
      case "create_session":
        return toContent(handleCreateSession(args as unknown as Parameters<typeof handleCreateSession>[0]));
      case "send_message":
        return toContent(handleSendMessage(args as unknown as Parameters<typeof handleSendMessage>[0]));
      case "get_messages":
        return toContent(handleGetMessages(args as unknown as Parameters<typeof handleGetMessages>[0]));
      case "wait_for_message":
        return toContent(handleWaitForMessage(args as unknown as Parameters<typeof handleWaitForMessage>[0]));
      case "get_toasts":
        return toContent(handleGetToasts(args as unknown as Parameters<typeof handleGetToasts>[0]));
      case "wait_for_toast":
        return toContent(handleWaitForToast(args as unknown as Parameters<typeof handleWaitForToast>[0]));
      case "take_screenshot":
        return toContent(handleTakeScreenshot(args as unknown as Parameters<typeof handleTakeScreenshot>[0]));
      case "get_tui_state":
        return toContent(handleGetTuiState(args as unknown as Parameters<typeof handleGetTuiState>[0]));
      default:
        throw new Error(`Unknown tool: ${name}`);
    }
  });

  return server;
}

export async function runServer(): Promise<void> {
  const server = createServer();
  const transport = new StdioServerTransport();

  await server.connect(transport);

  console.error("OpenCode MCP Server running on stdio");
}
