import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StdioClientTransport } from "@modelcontextprotocol/sdk/client/stdio.js";
import { describe, it, expect, beforeAll, afterAll } from "@jest/globals";

describe("Boulder Idle Detection", () => {
  let client: Client;
  let transport: StdioClientTransport;

  beforeAll(async () => {
    transport = new StdioClientTransport({
      command: "node",
      args: ["../server/dist/index.js"]
    });

    client = new Client({ name: "boulder-test", version: "1.0.0" });
    await client.connect(transport);
  });

  afterAll(async () => {
    await client.close();
  });

  it("should detect 30 seconds of idle time", async () => {
    const session = await client.callTool({
      name: "create_session",
      arguments: {
        directory: "/home/newman/magic/nexus",
        agent: "boulder-test"
      }
    });

    const sessionId = session.content[0].text;
    const parsedSession = JSON.parse(sessionId);
    const actualSessionId = parsedSession.session?.id || sessionId;

    await client.callTool({
      name: "send_message",
      arguments: {
        sessionId: actualSessionId,
        content: "Starting boulder idle detection test",
        waitForResponse: false
      }
    });

    const toast = await client.callTool({
      name: "wait_for_toast",
      arguments: {
        sessionId: actualSessionId,
        pattern: "BOULDER ENFORCEMENT",
        timeout: 35000
      }
    });

    expect(toast.content[0].text).toContain("BOULDER ENFORCEMENT");

    const messages = await client.callTool({
      name: "get_messages",
      arguments: {
        sessionId: actualSessionId,
        limit: 10
      }
    });

    const messageData = JSON.parse(messages.content[0].text);
    const hasBoulderMessage = messageData.some((m: any) =>
      m.content?.includes("BOULDER ENFORCEMENT")
    );

    expect(hasBoulderMessage).toBe(true);

    await client.callTool({
      name: "close_session",
      arguments: { sessionId: actualSessionId }
    });
  }, 40000);
});
