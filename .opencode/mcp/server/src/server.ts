import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { createSessionTool } from "./tools/session.js";

export function createServer(): Server {
  const server = new Server(
    {
      name: "@nexus/opencode-mcp-server",
      version: "0.1.0",
    },
    {
      capabilities: {
        tools: {},
      },
    }
  );

  const sessionTool = createSessionTool();
  server.setToolHandler(sessionTool);

  return server;
}

export async function runServer(): Promise<void> {
  const server = createServer();
  const transport = new StdioServerTransport();

  await server.connect(transport);

  console.error("OpenCode MCP Server running on stdio");
}
