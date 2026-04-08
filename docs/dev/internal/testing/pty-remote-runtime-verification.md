# PTY Remote Runtime Verification (Firecracker + LXC)

Date: 2026-04-08
Branch: `feat/firecracker-guest-networking`

## Goal

Verify that clicking **Terminal** in Nexus UI opens a PTY session in the remote runtime guest context for `firecracker` and `lxc` workspaces, instead of falling back to host PTY.

Note: transport is runtime agent shell RPC (`shell.open/write/resize/close`) over runtime connector, not SSH protocol.

## Scope

- Server-side PTY backend routing for non-local workspaces.
- Remote shell open semantics (`bash`, `/workspace`) for `firecracker` and `lxc`.
- Regression coverage to prevent host fallback for non-local backends.

## Code Paths Under Test

- `packages/nexus/pkg/server/server.go`
  - `handlePTYOpen`
  - `handleFirecrackerPTYOpen`
  - `handlePTYWrite`
  - `handlePTYResize`
  - `handlePTYClose`

## Targeted Verification Commands

Run from `packages/nexus`:

```bash
go test ./pkg/server -run PTYOpenUsesRemoteConnectorForFirecrackerAndLXC -v
go test ./pkg/server -v
```

## Expected Outcomes

1. For backend `firecracker`, `pty.open` calls remote connector path (not host `pty.StartWithSize`).
2. For backend `lxc`, `pty.open` also calls remote connector path (not host fallback).
3. Remote shell-open request uses:
   - command: `bash`
   - workdir: `/workspace`
4. Server test suite remains green after routing changes.

## Observed Results

- `TestPTYOpenUsesRemoteConnectorForFirecrackerAndLXC/firecracker`: PASS
- `TestPTYOpenUsesRemoteConnectorForFirecrackerAndLXC/lxc`: PASS
- Assertions confirmed:
  - connector invoked for workspace ID,
  - `shell.open.command == "bash"`,
  - `shell.open.workdir == "/workspace"`.

## Conclusion

PTY routing for UI terminal sessions is verified as remote-runtime for both `firecracker` and `lxc` backends in server logic. This closes the previous behavior where `lxc` could fall through to host PTY.

## Follow-up (Recommended)

Add an end-to-end browser-driven check (UI click -> backend RPC trace) that inspects `pty.open` traffic and confirms non-local backend routing in a live daemon session.
