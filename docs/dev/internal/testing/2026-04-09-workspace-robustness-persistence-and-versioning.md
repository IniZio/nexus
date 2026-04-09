# Workspace Robustness + Persistence + Version Guardrails Verification

Date: 2026-04-09

## Scope

This report captures verification evidence for:

1. Worktree/fork robustness under stale local metadata.
2. Daemon spotlight persistence across restart.
3. Node-level daemon compatibility guardrail surfacing and sync-session collision guard.

## Branch and Commits

- Branch: `feat/workspace-robustness-persistence-versioning`
- Commits:
  - `fix(workspace): harden worktree sync and persist spotlight`
  - `feat(version): add daemon compatibility and sync guards`

## SQLite-Only Persistence Cutover Decisions

- Legacy JSON migration: none.
- Empty sqlite on upgrade: silent empty state.
- Legacy JSON codepaths removed.

Notes:

- Spotlight persistence now relies on sqlite repository hydration/persistence.
- Runtime verification intentionally uses non-`8080` port `:8101` and an isolated workspace dir due to known local `:8080` collision risk.

## Verification Commands and Results

### 1) Core package regression suite

Command:

```bash
go test ./pkg/store ./pkg/workspacemgr ./pkg/spotlight ./pkg/server ./pkg/handlers ./pkg/config ./cmd/nexus -count=1
```

Observed:

```text
Go test: 242 passed in 7 packages
```

Note:

- `pkg/localws` robustness is validated separately in section 3.

### 2) Runtime proof (sqlite-backed spotlight persistence survives restart)

### Environment

- Runtime proof daemon binary: `/tmp/nexus-proof-daemon`
- Runtime proof CLI binary: `/tmp/nexus-proof-cli`
- RPC helper: `/tmp/nexus-proof-rpc/rpc_once.cjs`
- Proof daemon endpoint: `ws://127.0.0.1:8101` / `http://127.0.0.1:8101/healthz`
- Proof daemon workspace dir: `/tmp/nexus-proof-clean-8101`

### Health check (before proof)

Command:

```bash
curl -sSf http://127.0.0.1:8101/healthz
```

Observed:

```json
{"ok":true,"service":"workspace-daemon"}
```

### Workspace creation for proof

Command:

```bash
NEXUS_DAEMON_PORT=8101 NEXUS_DAEMON_TOKEN=dev-token /tmp/nexus-proof-cli workspace create --repo /Users/newman/magic/nexus/.case-studies/hanlun-lms --name proof-spotlight-clean-8101
```

Observed key lines:

```text
created workspace proof-spotlight-clean-8101  (id: ws-1775748248662477000)
local worktree:   /Users/newman/nexus-workspaces/proof-spotlight-clean-8101
mutagen session:  nexus-ws-1775748248662477000
```

### Spotlight before apply

Command:

```bash
node /tmp/nexus-proof-rpc/rpc_once.cjs ws://127.0.0.1:8101 dev-token spotlight.list '{"workspaceId":"ws-1775748248662477000"}'
```

Observed:

```json
{"jsonrpc":"2.0","id":"1","result":{"forwards":[]}}
```

### Apply forwards

Command:

```bash
node /tmp/nexus-proof-rpc/rpc_once.cjs ws://127.0.0.1:8101 dev-token spotlight.expose '{"spec":{"workspaceId":"ws-1775748248662477000","service":"proof-web","remotePort":3000,"localPort":18101,"host":"127.0.0.1"}}'
node /tmp/nexus-proof-rpc/rpc_once.cjs ws://127.0.0.1:8101 dev-token spotlight.expose '{"spec":{"workspaceId":"ws-1775748248662477000","service":"proof-api","remotePort":4000,"localPort":18102,"host":"127.0.0.1"}}'
```

Observed (trimmed):

```json
{"jsonrpc":"2.0","id":"1","result":{"forward":{"id":"spot-1775748256731803000","workspaceId":"ws-1775748248662477000","service":"proof-web","remotePort":3000,"localPort":18101,"host":"127.0.0.1","createdAt":"2026-04-09T15:24:16.731803Z"}}}
{"jsonrpc":"2.0","id":"1","result":{"forward":{"id":"spot-1775748256927558000","workspaceId":"ws-1775748248662477000","service":"proof-api","remotePort":4000,"localPort":18102,"host":"127.0.0.1","createdAt":"2026-04-09T15:24:16.927558Z"}}}
```

Forward count after apply: `2`.

### Spotlight after apply

Command:

```bash
node /tmp/nexus-proof-rpc/rpc_once.cjs ws://127.0.0.1:8101 dev-token spotlight.list '{"workspaceId":"ws-1775748248662477000"}'
```

Observed (trimmed):

```json
{"jsonrpc":"2.0","id":"1","result":{"forwards":[{"id":"spot-1775748256731803000"},{"id":"spot-1775748256927558000"}]}}
```

Forward count after apply list: `2`.

### Restart daemon and verify persistence

Commands:

```bash
kill $(cat /tmp/nexus-proof-clean-8101.pid)
nohup /tmp/nexus-proof-daemon --port 8101 --token dev-token --workspace-dir /tmp/nexus-proof-clean-8101 > /tmp/nexus-proof-clean-8101.log 2>&1 &
curl -sSf http://127.0.0.1:8101/healthz
node /tmp/nexus-proof-rpc/rpc_once.cjs ws://127.0.0.1:8101 dev-token spotlight.list '{"workspaceId":"ws-1775748248662477000"}'
```

Observed:

```json
{"ok":true,"service":"workspace-daemon"}
```

Observed (trimmed):

```json
{"jsonrpc":"2.0","id":"1","result":{"forwards":[{"id":"spot-1775748256731803000"},{"id":"spot-1775748256927558000"}]}}
```

Forward count after restart list: `2`.

Interpretation:

- Spotlight forwards remained present after daemon restart with the same workspace dir, proving sqlite-backed persistence load/save behavior works in live runtime conditions.

### 3) Targeted robustness tests (localws + workspacemgr)

Command:

```bash
go test ./pkg/workspacemgr ./pkg/localws -count=1
```

Observed:

```text
Go test: 28 passed in 2 packages
```

Coverage proved:

- Stale non-git local worktree directory is removed and recreated as a valid git worktree.
- Fork path resolution ignores stale `LocalWorktreePath` metadata and falls back to inferred repo worktree path.

### 4) Spotlight persistence tests (manager + server restart persistence)

Command:

```bash
go test ./pkg/spotlight ./pkg/server -count=1
```

Observed:

```text
Go test: 17 passed in 2 packages
```

Coverage proved:

- Spotlight manager save/load roundtrip preserves forwards.
- Server hydrates spotlight forwards from sqlite-backed repository on startup.
- Spotlight mutations are persisted through sqlite-backed repository writes.

### 5) Compatibility/sync guard tests (config + handlers)

Command:

```bash
go test ./pkg/config ./pkg/handlers -count=1
```

Observed:

```text
Go test: 85 passed in 2 packages
```

Coverage proved:

- Node config accepts valid `compatibility.minimumDaemonVersion` values (semver-like).
- Node config rejects invalid `minimumDaemonVersion` strings.
- `workspace.setLocalWorktree` clears duplicate mutagen session IDs from older workspace records when a session is re-bound.

## What Is Now Robust

1. Local worktree setup is resilient to stale directory drift.
2. Workspace fork behavior is resilient to stale parent `LocalWorktreePath` metadata.
3. Spotlight forwarding state survives daemon restart via sqlite-backed persistence.
4. Node compatibility metadata is validated and surfaced via node info handler.
5. Mutagen session ownership collisions are auto-healed on local worktree updates.

## Known Limitations

1. Compatibility guardrail currently validates and surfaces `minimumDaemonVersion`, but does not yet enforce hard daemon/CLI startup refusal based on semantic version comparison.
2. Spotlight persistence currently stores metadata state; it does not re-open external system tunnels/processes beyond recorded forward state in this layer.
