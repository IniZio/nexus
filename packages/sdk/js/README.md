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
- `client.spotlight.expose/list/close/applyDefaults/applyComposePorts`
- `client.workspace.create/open/list/start/stop/restore/pause/resume/fork/remove`

## Workspace lifecycle example

```ts
const created = await client.workspace.create({
  repo: 'git@github.com:org/repo.git',
  ref: 'main',
  workspaceName: 'feature-ui',
  agentProfile: 'default',
})

await client.workspace.start(created.id)
await client.workspace.pause(created.id)
await client.workspace.resume(created.id)
await client.workspace.stop(created.id)
await client.workspace.restore(created.id)
```

## Docs

- `docs/reference/sdk.md`
- `docs/reference/cli.md`

## License

MIT
