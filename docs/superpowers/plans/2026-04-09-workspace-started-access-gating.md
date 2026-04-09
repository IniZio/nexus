# Workspace Started Access Gating Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enforce that interactive workspace access is blocked until a workspace is explicitly started, and add `nexus workspace start <id>` as the operator path to transition access.

**Architecture:** Add one server-side state guard for access-sensitive RPC endpoints and invoke it before PTY and readiness flows. Add a dedicated CLI `workspace start` command that calls `workspace.start` RPC, then wire it into the workspace subcommand dispatcher and usage text. Lock behavior with focused tests in server and CLI packages, then verify end-to-end on `:8080`.

**Tech Stack:** Go, JSON-RPC over WebSocket, Cobra-style custom CLI dispatch in `cmd/nexus`, Go test tooling.

---

### Task 1: Add explicit RPC error for non-started workspace access

**Files:**
- Modify: `packages/nexus/pkg/rpcerrors/errors.go`
- Test: `packages/nexus/pkg/server/server_test.go`

- [ ] **Step 1: Write the failing test asserting access denial uses a dedicated error message**

```go
func TestPTYOpenRejectsWorkspaceNotStarted(t *testing.T) {
	srv, err := NewServer(0, t.TempDir(), "secret-token")
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	ws := createWorkspaceForPTYTest(t, srv.workspaceMgr, "") // created state by default
	conn := &Connection{send: make(chan []byte, 16), clientID: "test", pty: map[string]*ptySession{}}

	payload, _ := json.Marshal(map[string]any{"workspaceId": ws.ID})
	_, rpcErr := srv.handlePTYOpen(payload, conn, srv.ws)
	if rpcErr == nil {
		t.Fatal("expected workspace-not-started rpc error")
	}
	if rpcErr.Message != "Workspace not started" {
		t.Fatalf("expected Workspace not started, got %+v", rpcErr)
	}
}
```

- [ ] **Step 2: Run the targeted test to verify failure before implementation**

Run: `go test ./packages/nexus/pkg/server -run TestPTYOpenRejectsWorkspaceNotStarted -count=1`
Expected: FAIL because `handlePTYOpen` currently allows `created` state.

- [ ] **Step 3: Add the new reusable RPC error constant**

```go
var (
	ErrInvalidToken          = &RPCError{Code: -32001, Message: "Invalid authentication token"}
	ErrUnauthorized          = &RPCError{Code: -32002, Message: "Unauthorized"}
	ErrAuthRelayInvalid      = &RPCError{Code: -32008, Message: "Invalid auth relay token"}
	ErrAuthBindingAbsent     = &RPCError{Code: -32009, Message: "Auth binding not found"}
	ErrMethodNotFound        = &RPCError{Code: -32601, Message: "Method not found"}
	ErrInvalidParams         = &RPCError{Code: -32602, Message: "Invalid params"}
	ErrInternalError         = &RPCError{Code: -32603, Message: "Internal error"}
	ErrFileNotFound          = &RPCError{Code: -32003, Message: "File not found"}
	ErrPermissionDenied      = &RPCError{Code: -32004, Message: "Permission denied"}
	ErrTimeout               = &RPCError{Code: -32005, Message: "Command timeout"}
	ErrInvalidPath           = &RPCError{Code: -32006, Message: "Invalid path"}
	ErrWorkspaceNotFound     = &RPCError{Code: -32007, Message: "Workspace not found"}
	ErrWorkspaceNotStarted   = &RPCError{Code: -32010, Message: "Workspace not started"}
)
```

- [ ] **Step 4: Re-run the test to keep it failing for the right reason**

Run: `go test ./packages/nexus/pkg/server -run TestPTYOpenRejectsWorkspaceNotStarted -count=1`
Expected: still FAIL, but now implementation can reference the new error constant.

- [ ] **Step 5: Commit error constant addition (if implemented separately)**

```bash
git add packages/nexus/pkg/rpcerrors/errors.go
git commit -m "fix(rpc): add workspace-not-started error"
```

### Task 2: Enforce server-side started-state guard for access endpoints

**Files:**
- Modify: `packages/nexus/pkg/server/server.go`
- Modify: `packages/nexus/pkg/server/server_test.go`

- [ ] **Step 1: Write failing tests for deny-before-start and allow-after-start**

```go
func TestWorkspaceReadyRejectsWorkspaceNotStarted(t *testing.T) {
	srv, err := NewServer(0, t.TempDir(), "secret-token")
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	ws := createWorkspaceForPTYTest(t, srv.workspaceMgr, "")
	msg := &RPCMessage{
		JSONRPC: "2.0",
		ID:      "1",
		Method:  "workspace.ready",
		Params: mustRawJSON(map[string]any{
			"workspaceId": ws.ID,
			"checks": []map[string]any{{
				"name":    "ok",
				"command": "sh",
				"args":    []string{"-lc", "exit 0"},
			}},
		}),
	}

	resp := srv.processRPC(msg, &Connection{send: make(chan []byte, 1), pty: map[string]*ptySession{}})
	if resp.Error == nil {
		t.Fatal("expected workspace-not-started error")
	}
	if resp.Error.Message != "Workspace not started" {
		t.Fatalf("unexpected rpc error: %+v", resp.Error)
	}
}

func TestPTYOpenAllowsStartedWorkspace(t *testing.T) {
	srv, err := NewServer(0, t.TempDir(), "secret-token")
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	ws := createWorkspaceForPTYTest(t, srv.workspaceMgr, "")
	if err := srv.workspaceMgr.Start(ws.ID); err != nil {
		t.Fatalf("start workspace: %v", err)
	}

	conn := &Connection{send: make(chan []byte, 16), clientID: "test", pty: map[string]*ptySession{}}
	payload, _ := json.Marshal(map[string]any{"workspaceId": ws.ID, "shell": "sh", "rows": 12, "cols": 40})
	result, rpcErr := srv.handlePTYOpen(payload, conn, srv.ws)
	if rpcErr != nil {
		t.Fatalf("expected started workspace to open PTY, got %+v", rpcErr)
	}
	if result == nil {
		t.Fatal("expected pty.open result")
	}
}

func mustRawJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
```

- [ ] **Step 2: Run targeted server tests to confirm they fail**

Run: `go test ./packages/nexus/pkg/server -run 'TestWorkspaceReadyRejectsWorkspaceNotStarted|TestPTYOpenRejectsWorkspaceNotStarted|TestPTYOpenAllowsStartedWorkspace' -count=1`
Expected: FAIL because no state guard exists yet.

- [ ] **Step 3: Implement a shared guard helper and use it in RPC access paths**

```go
func (s *Server) requireWorkspaceStarted(workspaceID string) *rpckit.RPCError {
	if strings.TrimSpace(workspaceID) == "" {
		return rpckit.ErrInvalidParams
	}
	wsRecord, ok := s.workspaceMgr.Get(workspaceID)
	if !ok {
		return rpckit.ErrWorkspaceNotFound
	}
	if wsRecord.State != workspacemgr.StateRunning {
		return rpckit.ErrWorkspaceNotStarted
	}
	return nil
}
```

Then apply it in:

```go
case "workspace.ready":
	workspaceID := extractWorkspaceID(msg.Params)
	if workspaceID == "" {
		workspaceID = workspace.ID()
	}
	if rpcErr := s.requireWorkspaceStarted(workspaceID); rpcErr != nil {
		err = rpcErr
		break
	}
	s.ensureComposeForwards(ctx, workspaceID, workspace.Path())
	result, err = handlers.HandleWorkspaceReady(ctx, msg.Params, workspace, s.serviceMgr)

case "pty.open":
	result, err = s.handlePTYOpen(msg.Params, conn, workspace)
```

And in `handlePTYOpen` before starting shell/session:

```go
workspaceID := strings.TrimSpace(p.WorkspaceID)
if workspaceID != "" {
	if rpcErr := s.requireWorkspaceStarted(workspaceID); rpcErr != nil {
		return nil, rpcErr
	}
}
```

- [ ] **Step 4: Update existing PTY tests to start workspace before asserting success**

Add before `handlePTYOpen` calls in existing passing tests:

```go
if err := srv.workspaceMgr.Start(ws.ID); err != nil {
	t.Fatalf("start workspace: %v", err)
}
```

- [ ] **Step 5: Run server package tests and verify success**

Run: `go test ./packages/nexus/pkg/server -count=1`
Expected: PASS.

- [ ] **Step 6: Commit server gating changes**

```bash
git add packages/nexus/pkg/server/server.go packages/nexus/pkg/server/server_test.go packages/nexus/pkg/rpcerrors/errors.go
git commit -m "fix(server): gate access on started workspaces"
```

### Task 3: Add `nexus workspace start <id>` command and dispatcher wiring

**Files:**
- Modify: `packages/nexus/cmd/nexus/workspace.go`
- Create: `packages/nexus/cmd/nexus/workspace_test.go`

- [ ] **Step 1: Write failing CLI tests for start command and usage text**

```go
func TestPrintWorkspaceUsageIncludesStart(t *testing.T) {
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	printWorkspaceUsage()
	_ = w.Close()
	os.Stderr = old

	buf, _ := io.ReadAll(r)
	got := string(buf)
	if !strings.Contains(got, "start <id>") {
		t.Fatalf("expected usage to include start <id>, got %q", got)
	}
}

func TestRunWorkspaceStartCommandCallsWorkspaceStartRPC(t *testing.T) {
	calledMethod := ""
	calledID := ""

	origEnsure := ensureDaemonFn
	origRPC := daemonRPCFn
	t.Cleanup(func() {
		ensureDaemonFn = origEnsure
		daemonRPCFn = origRPC
	})

	ensureDaemonFn = func() (*websocket.Conn, error) { return nil, nil }
	daemonRPCFn = func(_ *websocket.Conn, method string, params any, out any) error {
		calledMethod = method
		m := params.(map[string]any)
		calledID, _ = m["id"].(string)
		return nil
	}

	runWorkspaceStartCommand([]string{"ws-123"})

	if calledMethod != "workspace.start" {
		t.Fatalf("expected workspace.start method, got %q", calledMethod)
	}
	if calledID != "ws-123" {
		t.Fatalf("expected workspace id ws-123, got %q", calledID)
	}
}
```

- [ ] **Step 2: Run targeted CLI tests to confirm they fail before implementation**

Run: `go test ./packages/nexus/cmd/nexus -run 'TestPrintWorkspaceUsageIncludesStart|TestRunWorkspaceStartCommandCallsWorkspaceStartRPC' -count=1`
Expected: FAIL because command/seams do not exist yet.

- [ ] **Step 3: Introduce small test seams for daemon connect/RPC in `workspace.go`**

```go
var ensureDaemonFn = ensureDaemon
var daemonRPCFn = daemonRPC
```

Replace direct calls in new `runWorkspaceStartCommand` with seam vars:

```go
func runWorkspaceStartCommand(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: nexus workspace start <id>")
		os.Exit(2)
	}
	conn, err := ensureDaemonFn()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus workspace start: %v\n", err)
		os.Exit(1)
	}
	if conn != nil {
		defer conn.Close()
	}

	if err := daemonRPCFn(conn, "workspace.start", map[string]any{"id": args[0]}, nil); err != nil {
		fmt.Fprintf(os.Stderr, "nexus workspace start: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("started workspace %s\n", args[0])
}
```

- [ ] **Step 4: Wire command into dispatcher and usage text**

```go
switch sub {
case "list", "ls":
	runWorkspaceListCommand(rest)
case "create":
	runWorkspaceCreateCommand(rest)
case "start":
	runWorkspaceStartCommand(rest)
case "stop":
	runWorkspaceStopCommand(rest)
...
}
```

Usage block update:

```text
  start <id>            start a workspace and make it accessible
```

- [ ] **Step 5: Run full CLI package tests**

Run: `go test ./packages/nexus/cmd/nexus -count=1`
Expected: PASS.

- [ ] **Step 6: Commit CLI changes**

```bash
git add packages/nexus/cmd/nexus/workspace.go packages/nexus/cmd/nexus/workspace_test.go
git commit -m "feat(cli): add workspace start command"
```

### Task 4: End-to-end verification on `:8080` with evidence

**Files:**
- Modify: `docs/dev/internal/testing/2026-04-09-workspace-started-access-gating.md`

- [ ] **Step 1: Build CLI/daemon binary before runtime verification**

Run: `go build ./packages/nexus/cmd/nexus ./packages/nexus/cmd/daemon`
Expected: command exits 0.

- [ ] **Step 2: Start daemon on `:8080` and confirm health endpoint**

Run:

```bash
NEXUS_DAEMON_PORT=8080 NEXUS_DAEMON_TOKEN=dev-token go run ./packages/nexus/cmd/daemon > /tmp/nexus-daemon-8080.log 2>&1 &
curl -sSf http://localhost:8080/healthz
```

Expected: `{"ok":true,"service":"workspace-daemon"}`.

- [ ] **Step 3: Create workspace and prove initial state is created**

Run:

```bash
NEXUS_DAEMON_PORT=8080 NEXUS_DAEMON_TOKEN=dev-token nexus workspace create --repo .case-studies/hanlun-lms --name started-gate-e2e
NEXUS_DAEMON_PORT=8080 NEXUS_DAEMON_TOKEN=dev-token nexus workspace list
```

Expected: new workspace row with state `created`.

- [ ] **Step 4: Attempt PTY and readiness access before start and capture denial evidence**

Run (readiness check uses requested default command style):

```bash
node /tmp/nexus-pty-state-check.js
node -e 'const WebSocket=require("ws");const ws=new WebSocket("ws://localhost:8080/?token=dev-token");ws.on("open",()=>ws.send(JSON.stringify({jsonrpc:"2.0",id:"1",method:"workspace.ready",params:{workspaceId:process.argv[1],checks:[{name:"compose",command:"docker-compose",args:["ps"]}],timeoutMs:500,intervalMs:100}})));ws.on("message",m=>{console.log(String(m));process.exit(0);});' "$WS_ID"
```

Expected: RPC error with `Workspace not started` for both access paths.

- [ ] **Step 5: Start workspace via CLI and verify state transition**

Run:

```bash
NEXUS_DAEMON_PORT=8080 NEXUS_DAEMON_TOKEN=dev-token nexus workspace start "$WS_ID"
NEXUS_DAEMON_PORT=8080 NEXUS_DAEMON_TOKEN=dev-token nexus workspace list
```

Expected: state for `$WS_ID` becomes `running`.

- [ ] **Step 6: Re-run PTY/readiness access and capture success evidence**

Run:

```bash
node /tmp/nexus-pty-state-check.js
node -e 'const WebSocket=require("ws");const ws=new WebSocket("ws://localhost:8080/?token=dev-token");ws.on("open",()=>ws.send(JSON.stringify({jsonrpc:"2.0",id:"1",method:"workspace.ready",params:{workspaceId:process.argv[1],checks:[{name:"compose",command:"docker-compose",args:["ps"]}],timeoutMs:500,intervalMs:100}})));ws.on("message",m=>{console.log(String(m));process.exit(0);});' "$WS_ID"
```

Expected: PTY open returns `sessionId`; readiness returns structured `ready` result instead of not-started error.

- [ ] **Step 7: Write verification notes and key logs**

Write `docs/dev/internal/testing/2026-04-09-workspace-started-access-gating.md` including:

```md
- daemon command used on :8080
- workspace id used for verification
- deny-before-start evidence (command + rpc error)
- allow-after-start evidence (command + success payload)
- note if any local 8080 collisions occurred and how they were resolved
```

- [ ] **Step 8: Run focused regression tests + formatting checks and commit verification doc**

Run:

```bash
go test ./packages/nexus/pkg/server ./packages/nexus/cmd/nexus -count=1
```

Expected: PASS.

Commit:

```bash
git add docs/dev/internal/testing/2026-04-09-workspace-started-access-gating.md
git commit -m "docs(testing): record started-access gating evidence"
```

## Final Verification Checklist

- [ ] `go test ./packages/nexus/pkg/server -count=1`
- [ ] `go test ./packages/nexus/cmd/nexus -count=1`
- [ ] `go build ./packages/nexus/cmd/nexus ./packages/nexus/cmd/daemon`
- [ ] Manual evidence captured on `:8080` for deny-before-start and allow-after-start
- [ ] CLI help output includes `workspace start`
- [ ] No endpoint in scope allows interactive access while workspace state is `created`

## Scope and Consistency Self-Review

- Spec coverage: server access gate, explicit start command, stable not-started error, and `:8080` runtime verification are all mapped to tasks.
- Placeholder scan: no `TODO/TBD` placeholders; every code/edit/test step has concrete file paths and command lines.
- Type consistency: state constant used is `workspacemgr.StateRunning`, matching existing manager `Start` behavior.
