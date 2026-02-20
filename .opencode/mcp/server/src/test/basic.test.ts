import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StdioClientTransport } from "@modelcontextprotocol/sdk/client/stdio.js";

async function testMCP() {
  const transport = new StdioClientTransport({
    command: "node",
    args: ["dist/index.js"]
  });

  const client = new Client({ name: "test-client", version: "1.0.0" });
  await client.connect(transport);

  const result = await client.callTool({
    name: "create_session",
    arguments: {
      directory: "/tmp/test",
      agent: "test"
    }
  });

  console.log("Session created:", result);

  await client.close();
}

testMCP().catch(console.error);
