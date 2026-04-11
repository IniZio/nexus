# @nexus/sdk

TypeScript client for the Nexus workspace daemon (`WorkspaceClient`, `workspaces`, `ssh`).

```bash
pnpm add @nexus/sdk
```

```typescript
import { WorkspaceClient } from '@nexus/sdk';

const client = new WorkspaceClient({
  endpoint: 'ws://localhost:8080',
  token: process.env.NEXUS_TOKEN ?? 'dev-token',
});
await client.connect();
const ws = await client.workspaces.open('ws-example');
console.log((await ws.exec.exec('bash', ['-lc', 'echo ok'])).stdout);
await client.disconnect();
```

Canonical API and remote-deployment notes: `docs/reference/sdk.md`, `docs/reference/cli.md`.

MIT