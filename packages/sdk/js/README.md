# @nexus/sdk
# Nexus SDK (`packages/sdk/js`)

TypeScript SDK for talking to the Nexus workspace daemon.

## Install

```bash
pnpm add @nexus/sdk
```

## Minimal example

```ts
import { WorkspaceClient } from '@nexus/sdk'

const client = new WorkspaceClient({
  endpoint: 'ws://localhost:8080',
  workspaceId: 'example-workspace',
  token: process.env.NEXUS_TOKEN ?? 'dev-token',
})

await client.connect()
const result = await client.exec('bash', ['-lc', 'echo sdk-ok'])
console.log(result.stdout)
await client.disconnect()
```

## Common operations

- `client.fs.readFile`, `client.fs.writeFile`, `client.fs.readdir`
- `client.exec(command, args, options)`
- `client.workspace.create/open/list/remove`
- `workspace.spotlight.expose/list/close`

## Docs

- `docs/reference/workspace-sdk.md`
- `docs/reference/workspace-daemon.md`

## License

MIT
