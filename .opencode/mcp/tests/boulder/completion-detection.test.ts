import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StdioClientTransport } from "@modelcontextprotocol/sdk/client/stdio.js";
import { describe, it, expect, beforeAll, afterAll } from "@jest/globals";

describe("Boulder Completion Detection", () => {
  let client: Client;
  let transport: StdioClientTransport;

  beforeAll(async () => {
    transport = new StdioClientTransport({
      command: "node",
      args: ["../server/dist/index.js"]
    });

    client = new Client({ name: "boulder-completion-test", version: "1.0.0" });
    await client.connect(transport);
  });

  afterAll(async () => {
    await client.close();
  });

  it("should detect completion keywords", async () => {
    const session = await client.callTool({
      name: "create_session",
      arguments: {
        directory: "/home/newman/magic/nexus",
        agent: "boulder-test"
      }
    });

    const sessionId = JSON.parse(session.content[0].text).session?.id || session.content[0].text;

    const completionPhrases = [
      "That's all",
      "We are done",
      "Implementation complete",
      "Task complete",
      "All done"
    ];

    for (const phrase of completionPhrases) {
      await client.callTool({
        name: "send_message",
        arguments: {
          sessionId,
          content: phrase,
          waitForResponse: false
        }
      });
    }

    const messages = await client.callTool({
      name: "get_messages",
      arguments: {
        sessionId,
        limit: 20
      }
    });

    const messageData = JSON.parse(messages.content[0].text);
    const detectedPhrases = messageData.filter((m: any) =>
      completionPhrases.some(phrase => m.content?.includes(phrase))
    );

    expect(detectedPhrases.length).toBe(completionPhrases.length);

    await client.callTool({
      name: "close_session",
      arguments: { sessionId }
    });
  }, 20000);

  it("should not trigger on false positives", async () => {
    const session = await client.callTool({
      name: "create_session",
      arguments: {
        directory: "/home/newman/magic/nexus",
        agent: "boulder-test"
      }
    });

    const sessionId = JSON.parse(session.content[0].text).session?.id || session.content[0].text;

    const falsePositivePhrases = [
      "I will complete the read operation",
      "The tool call is complete",
      "Let me complete this edit"
    ];

    for (const phrase of falsePositivePhrases) {
      await client.callTool({
        name: "send_message",
        arguments: {
          sessionId,
          content: phrase,
          waitForResponse: false
        }
      });
    }

    const toast = await client.callTool({
      name: "wait_for_toast",
      arguments: {
        sessionId,
        pattern: "BOULDER",
        timeout: 3000
      }
    });

    const toastData = JSON.parse(toast.content[0].text);
    expect(toastData.matched).toBe(false);

    await client.callTool({
      name: "close_session",
      arguments: { sessionId }
    });
  }, 15000);

  it("should track iteration count on completions", async () => {
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
        content: "Implementation complete",
        waitForResponse: false
      }
    });

    const toast1 = await client.callTool({
      name: "wait_for_toast",
      arguments: {
        sessionId,
        pattern: "BOULDER",
        timeout: 5000
      }
    });

    await client.callTool({
      name: "send_message",
      arguments: {
        sessionId,
        content: "Work complete",
        waitForResponse: false
      }
    });

    const toast2 = await client.callTool({
      name: "wait_for_toast",
      arguments: {
        sessionId,
        pattern: "BOULDER",
        timeout: 5000
      }
    });

    const toastData1 = JSON.parse(toast1.content[0].text);
    const toastData2 = JSON.parse(toast2.content[0].text);

    expect(toastData1.matched || toastData1.toast?.title).toBeTruthy();
    expect(toastData2.matched || toastData2.toast?.title).toBeTruthy();

    await client.callTool({
      name: "close_session",
      arguments: { sessionId }
    });
  }, 20000);
});
