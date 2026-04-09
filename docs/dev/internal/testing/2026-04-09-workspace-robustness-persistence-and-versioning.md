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

## Verification Commands and Results

### 1) Targeted robustness tests (localws + workspacemgr)

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

### 2) Spotlight persistence tests (manager + server restart persistence)

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
- Server loads persisted spotlight forwards on startup from `.nexus/state/spotlight-forwards.json`.
- Server shutdown persists in-memory spotlight forwards back to disk.

### 3) Compatibility/sync guard tests (config + handlers)

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

### 4) Full touched-area regression

Command:

```bash
go test ./pkg/config ./pkg/handlers ./pkg/localws ./pkg/workspacemgr ./pkg/spotlight ./pkg/server ./cmd/nexus -count=1
```

Observed:

```text
Go test: 229 passed in 7 packages
```

## What Is Now Robust

1. Local worktree setup is resilient to stale directory drift.
2. Workspace fork behavior is resilient to stale parent `LocalWorktreePath` metadata.
3. Spotlight forwarding state survives daemon restart via state file persistence.
4. Node compatibility metadata is validated and surfaced via node info handler.
5. Mutagen session ownership collisions are auto-healed on local worktree updates.

## Known Limitations

1. Compatibility guardrail currently validates and surfaces `minimumDaemonVersion`, but does not yet enforce hard daemon/CLI startup refusal based on semantic version comparison.
2. Spotlight persistence currently stores metadata state; it does not re-open external system tunnels/processes beyond recorded forward state in this layer.
3. End-to-end runtime proof for backup/restore/fork using real daemons on `:8080` was not re-run in this report because this increment focused on deterministic package-level robustness and restart persistence tests.
