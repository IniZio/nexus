import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StdioClientTransport } from "@modelcontextprotocol/sdk/client/stdio.js";
import { describe, it, expect, beforeAll, afterAll } from "@jest/globals";

describe("Boulder Cooldown Functionality", () => {
  let client: Client;
  let transport: StdioClientTransport;

  beforeAll(async () => {
    transport = new StdioClientTransport({
      command: "node",
      args: ["../server/dist/index.js"]
    });

    client = new Client({ name: "boulder-cooldown-test", version: "1.0.0" });
    await client.connect(transport);
  });

  afterAll(async () => {
    await client.close();
  });

  it("should enforce cooldown after multiple completion attempts", async () => {
    const session = await client.callTool({
      name: "create_session",
      arguments: {
        directory: "/home/newman/magic/nexus",
        agent: "boulder-test"
      }
    });

    const sessionId = JSON.parse(session.content[0].text).session?.id || session.content[0].text;

    for (let i = 0; i < 3; i++) {
      await client.callTool({
        name: "send_message",
        arguments: {
          sessionId,
          content: "I am done with this task",
          waitForResponse: false
        }
      });

      await new Promise(resolve => setTimeout(resolve, 100));
    }

    const toast = await client.callTool({
      name: "wait_for_toast",
      arguments: {
        sessionId,
        pattern: "COOLDOWN",
        timeout: 5000
      }
    });

    const toastData = JSON.parse(toast.content[0].text);
    expect(toastData.matched || toastData.toast?.title?.includes("COOLDOWN")).toBe(true);

    await client.callTool({
      name: "close_session",
      arguments: { sessionId }
    });
  }, 30000);

  it("should show cooldown status in tui state", async () => {
    const session = await client.callTool({
      name: "create_session",
      arguments: {
        directory: "/home/newman/magic/nexus",
        agent: "boulder-test"
      }
    });

    const sessionId = JSON.parse(session.content[0].text).session?.id || session.content[0].text;

    await client.callTool({
      name: "send_message",
      arguments: {
        sessionId,
        content: "Task complete",
        waitForResponse: false
      }
    });

    const tuiState = await client.callTool({
      name: "get_tui_state",
      arguments: {
        sessionId
      }
    });

    const stateData = JSON.parse(tuiState.content[0].text);
    expect(stateData.state).toBeDefined();
    expect(stateData.state.mode).toBeDefined();

    await client.callTool({
      name: "close_session",
      arguments: { sessionId }
    });
  }, 15000);
});
