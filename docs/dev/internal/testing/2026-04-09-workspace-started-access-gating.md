# Workspace Started Access Gating Verification (`:8080`)

Date: 2026-04-09

## Environment

- Host endpoint used for verification: `ws://192.168.0.103:8080` (loopback `127.0.0.1:8080` was occupied by Cursor openresty)
- Daemon command:

```bash
go run ./cmd/daemon --port 8080 --token dev-token
```

- Health check (host IP):

```bash
curl -sS -i http://192.168.0.103:8080/healthz
```

Observed:

```text
HTTP/1.1 200 OK
{"ok":true,"service":"workspace-daemon"}
```

## Workspace IDs Used

- Primary started workspace from this run: `ws-1775734485513404000`
- Fresh deny/allow verification workspace: `ws-1775734619580698000`

## Create and Initial State Evidence

Create call:

```bash
node rpc_once.js ws://192.168.0.103:8080 dev-token workspace.create '{"spec":{"repo":"/Users/newman/magic/nexus/.case-studies/hanlun-lms","ref":"main","workspaceName":"started-gate-e2e-2","agentProfile":"default","backend":""}}'
```

Observed in result payload:

```json
{"id":"ws-1775734619580698000","state":"created"}
```

## Deny Before Start (Required)

PTY deny before start:

```bash
node rpc_once.js ws://192.168.0.103:8080 dev-token pty.open '{"workspaceId":"ws-1775734619580698000","shell":"bash","rows":24,"cols":80}'
```

Observed:

```json
{"error":{"code":-32010,"message":"Workspace not started"}}
```

Readiness deny before start (using requested default check command style):

```bash
node rpc_once.js ws://192.168.0.103:8080 dev-token workspace.ready '{"workspaceId":"ws-1775734619580698000","checks":[{"name":"compose","command":"docker-compose","args":["ps"]}],"timeoutMs":500,"intervalMs":100}'
```

Observed:

```json
{"error":{"code":-32010,"message":"Workspace not started"}}
```

## Start Transition

Start call:

```bash
node rpc_once.js ws://192.168.0.103:8080 dev-token workspace.start '{"id":"ws-1775734619580698000"}'
```

Observed:

```json
{"result":{"started":true}}
```

State confirmation (workspace list parse):

```text
ws-1775734619580698000 running
```

## Allow After Start (Required)

PTY allow after start:

```bash
node rpc_once.js ws://192.168.0.103:8080 dev-token pty.open '{"workspaceId":"ws-1775734619580698000","shell":"bash","rows":24,"cols":80}'
```

Observed:

```json
{"result":{"sessionId":"pty-1775734656469124000"}}
```

Readiness call after start (no longer blocked by not-started gate):

```bash
node rpc_once.js ws://192.168.0.103:8080 dev-token workspace.ready '{"workspaceId":"ws-1775734619580698000","checks":[{"name":"compose","command":"docker-compose","args":["ps"]}],"timeoutMs":500,"intervalMs":100}'
```

Observed:

```json
{"result":{"ready":false,"workspaceId":"ws-1775734619580698000","elapsedMs":612,"attempts":5,"lastResults":{"compose":1}}}
```

Interpretation: request is accepted and evaluated (returns result object), proving it is no longer denied by state-gating after `workspace.start`.

## Friction Notes

1. Local loopback `:8080` collision was present (`Cursor` openresty binding `127.0.0.1:8080`), so verification used host LAN IP `192.168.0.103:8080` where daemon listener (`*:8080`) responded.
2. Existing `packages/nexus/nexus` binary in repo root appears to be daemon binary in this workspace; for reproducible verification, RPC checks were executed directly over WebSocket using `rpc_once.js`.

## Regression + Build Verification

Working directory for both commands: `packages/nexus`.

Regression tests:

```bash
go test ./pkg/server ./cmd/nexus -count=1
```

Observed:

```text
Go test: 109 passed in 2 packages
```

Build verification:

```bash
go build ./cmd/nexus ./cmd/daemon
```

Observed:

```text
Go build: Success
```
