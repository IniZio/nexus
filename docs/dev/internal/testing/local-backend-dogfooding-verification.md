# Local Backend Dogfooding Verification

Date: 2026-04-01
Branch: `feat/firecracker-runtime-pivot`

## Goal

Validate that the `local` runtime backend supports practical Nexus self-use with parallel workspace operations and fork lineage, while preserving explicit backend selection constraints.

## Targeted Verification Commands

Run from `packages/nexus`:

```bash
go test ./pkg/runtime/local -run 'DogfoodingParallelWorkspaceOperations' -v
go test ./pkg/workspacemgr -run 'ForkParallelWorkspacesRemainIndependent' -v
go test ./pkg/handlers -run 'WorkspaceFork_WithFactoryLocalBackend' -v
go test ./pkg/runtime/... ./pkg/workspacemgr ./pkg/handlers -v
```

## Expected Outcomes

1. `pkg/runtime/local` parallel test passes:
   - two workspaces can run concurrently;
   - pause/resume no-op succeeds on known workspace;
   - fork creates child inheriting parent project root.
2. `pkg/workspacemgr` parallel/fork test passes:
   - parent-child lineage persisted via `ParentWorkspaceID`;
   - sibling workspace remains independent and running.
3. `pkg/handlers` local-factory fork test passes:
   - config-required `local` backend selected via factory capability;
   - child workspace backend remains `local`.
4. full targeted suites remain green.

## Observed Results

- `TestDriver_DogfoodingParallelWorkspaceOperations`: PASS
- `TestManager_ForkParallelWorkspacesRemainIndependent`: PASS
- `TestHandleWorkspaceFork_WithFactoryLocalBackend`: PASS
- Full suite command `go test ./pkg/runtime/... ./pkg/workspacemgr ./pkg/handlers -v`: PASS

## Conclusion

The `local` backend is suitable for v1 Nexus dogfooding of parallel workspace and fork workflows:

- explicit backend path works through runtime factory and handlers;
- workspace lineage and backend persistence remain correct;
- local driver behavior is deterministic and test-covered for create/start/pause/resume/fork/destroy flows.
