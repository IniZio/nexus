# Nexus Daemon — E2E Test Strategy

_Advisory document. Status: draft. Not committed to git._

---

## 1. Package Location Decision

**Recommendation: `packages/nexus/cmd/nexus-e2e/` — same Go module.**

### Trade-off table

| Criterion | Same module (`cmd/nexus-e2e/`) | Separate module (`packages/nexus-e2e/`) |
|---|---|---|
| Import harness types | ✅ Direct, no duplication | ❌ Must re-export or duplicate |
| Acts as a real client | ⚠️ Can accidentally import internals | ✅ Hard boundary |
| CI simplicity | ✅ One `go test ./...` scope | ❌ Two modules, two `go.mod` syncs |
| Remote-first compatibility | ✅ (subprocess + socket — no shared memory) | ✅ Same |
| Version pinning | ❌ Coupled to daemon version | ✅ Can pin independently |
| Maintenance overhead | Low | Higher (two modules, two dependency graphs) |

**Rationale:** The daemon is remote-first via subprocess + Unix socket — the test binary never imports `internal/` business logic at runtime, it only talks RPC. The same-module location gives free access to `internal/rpc/registry` and transport types for the harness client without duplication. A separate module's main benefit (hard import boundary) is already enforced by the RPC protocol itself. The maintenance overhead of two go.mod files is not worth it at this stage.

**Guard against internal leakage:** Add a linter rule (or `// noimports` comment convention) that `cmd/nexus-e2e/` must not import anything under `internal/app/` or `internal/domain/`. Harness may import `internal/rpc/registry` and `internal/transport` for the RPC client only.

---

## 2. Test Runner Architecture

```
cmd/nexus-e2e/
├── main.go              # flag parsing, suite runner, exit code
└── suite/
    ├── suite.go         # TestSuite struct, registration, Run
    └── case.go          # TestCase interface

test/e2e/
├── harness/
│   ├── harness.go       # Harness struct: spawn nexusd, connect, teardown
│   ├── client.go        # RPC client: Call(method, params) (any, error)
│   └── fixtures.go      # helpers: TempDB(), TempSocket(), TempWorkdir()
├── workspace/
│   ├── lifecycle_test.go
│   └── fork_test.go
├── spotlight/
│   └── spotlight_test.go
├── pty/
│   └── pty_test.go
├── fs/
│   └── fs_test.go
├── auth/
│   └── auth_test.go
└── daemon/
    └── info_test.go
```

### Harness contract (`test/e2e/harness/harness.go`)

```
type Harness struct {
    T          *testing.T
    SocketPath string
    DBPath     string
    Client     *Client
    cmd        *exec.Cmd
}

func New(t *testing.T, opts ...Option) *Harness
  // spawns nexusd with temp socket+db, waits for ready (polls node.info)
  // registers t.Cleanup to kill daemon + remove temps

func (h *Harness) Call(method string, params any) (json.RawMessage, error)
  // sends one JSON-RPC 2.0 request over Unix socket, returns raw result
```

### RPC client (`test/e2e/harness/client.go`)

```
type Client struct { conn net.Conn; enc *json.Encoder; dec *json.Decoder; mu sync.Mutex }

func Dial(socketPath string) (*Client, error)
func (c *Client) Call(method string, params any) (json.RawMessage, error)
  // newline-delimited JSON-RPC 2.0, incremental request IDs
func (c *Client) Close() error
```

### Readiness probe

After spawning nexusd, poll `node.info` with 100ms backoff, 5s timeout. Fail the test if daemon doesn't respond.

---

## 3. Coverage Matrix (prioritized by risk)

| Priority | Domain | Test | Why |
|---|---|---|---|
| P0 | Workspace | Create → Start → Stop → Remove lifecycle | Core path; regressions here break everything |
| P0 | Daemon | `node.info` health check | Harness readiness + smoke test |
| P1 | Workspace | Fork from existing workspace | Complex state machine; high regression risk |
| P1 | Workspace | Restore from snapshot | Snapshot correctness is critical for Firecracker |
| P1 | Auth relay | Mint token → use → revoke | Security-critical flow |
| P2 | PTY | Create session → resize → close | Common user flow |
| P2 | Spotlight | Start → ListForwards → CloseForward | Port forwarding correctness |
| P2 | FS | readFile / writeFile / readdir / stat | Basic guest file access |
| P3 | Project | CRUD | Lower risk but still user-facing |
| P3 | Workspace | Ready probe | Readiness polling logic |
| P4 | XCUITest | UI smoke (macOS only) | Optional; gated by `NEXUS_XCUI=1` |

---

## 4. File Organization

One file per risk cluster, not per method. Keep files ≤500 lines (transport/test layer limit).

```
test/e2e/
  workspace/lifecycle_test.go   # create/start/stop/remove + ready
  workspace/fork_test.go        # fork + checkout
  workspace/restore_test.go     # snapshot + restore
  spotlight/spotlight_test.go   # start/forward/close
  pty/pty_test.go               # create/resize/close
  fs/fs_test.go                 # read/write/stat/readdir
  auth/relay_test.go            # mint/revoke
  daemon/info_test.go           # node.info smoke
  project/project_test.go       # CRUD
```

Each file: one `TestXxx(t *testing.T)` top-level that calls `harness.New(t)` and subtests via `t.Run`.

---

## 5. Harness Contracts

The harness must expose exactly these — nothing more:

```go
// New spawns nexusd, returns ready harness. Cleanup registered automatically.
func New(t *testing.T, opts ...Option) *Harness

// Option knobs
func WithFirecracker(bin, kernel, rootfs string) Option  // enables FC backend
func WithNodeName(name string) Option

// Call sends one RPC and decodes result into out (json.Unmarshal).
func (h *Harness) Call(method string, params, out any) error

// MustCall fails the test immediately on any error.
func (h *Harness) MustCall(method string, params, out any)

// fixtures
func TempDB(t *testing.T) string        // temp sqlite path, auto-removed
func TempSocket(t *testing.T) string    // temp socket path, auto-removed
func TempWorkdir(t *testing.T) string   // temp directory, auto-removed
```

Individual test files must **not** construct `exec.Cmd` or `net.Conn` directly — all daemon interaction goes through `Harness`.

---

## 6. XCUITest Integration

- Gate with env var `NEXUS_XCUI=1` + build tag `//go:build xcui`
- Each XCUITest-requiring test checks `os.Getenv("NEXUS_XCUI") == ""` and calls `t.Skip("set NEXUS_XCUI=1 to run UI tests")`
- UI tests call `exec.Command("xcodebuild", "test", "-scheme", "NexusApp", ...)` as a subprocess
- Keep XCUITest invocations in a separate `test/e2e/xcui/` directory
- CI matrix: `nexus-e2e` job (always), `nexus-e2e-xcui` job (macOS runner only, manual trigger or nightly)

---

## 7. Parallelism

**Can run in parallel (independent state):**
- `daemon/info_test.go` — read-only
- `project/project_test.go` — isolated DB per harness instance
- `fs/fs_test.go` — isolated workdir per test
- `auth/relay_test.go` — isolated relay broker per harness

**Must serialize (shared lifecycle state):**
- `workspace/lifecycle_test.go` — workspace state machine (use `t.Run` sequential subtests within one harness)
- `workspace/fork_test.go` — depends on a parent workspace existing
- `workspace/restore_test.go` — depends on snapshot from a prior step

**Strategy:** Each top-level `TestXxx` gets its own `harness.New(t)` instance (own DB + socket). This means parallel top-level tests are safe. Subtests within a workspace lifecycle test run sequentially via `t.Run`.

Mark parallelizable top-level tests with `t.Parallel()`. Never call `t.Parallel()` inside workspace lifecycle subtests.

---

## 8. CI Integration

```yaml
# Build tag gates e2e compilation
//go:build e2e  (in all test/e2e/**/*_test.go files)

# Run e2e in CI
go test -tags e2e -timeout 5m ./test/e2e/...

# Environment variables
NEXUS_E2E_BINARY=/path/to/nexusd   # override daemon binary (default: build from source)
NEXUS_XCUI=1                        # enable XCUITest steps (macOS only)
NEXUS_E2E_TIMEOUT=300               # per-test timeout seconds

# Build nexusd first
go build -o /tmp/nexusd ./cmd/nexusd/
NEXUS_E2E_BINARY=/tmp/nexusd go test -tags e2e ./test/e2e/...
```

**CI job structure:**
1. `build` — `go build ./...` (excludes old pkg/ conflicts via build tags or exclude list)
2. `unit` — `go test ./internal/...`
3. `e2e` — build nexusd → `go test -tags e2e ./test/e2e/...` (Linux, no Firecracker, sandbox only)
4. `e2e-xcui` — macOS runner, nightly, `NEXUS_XCUI=1`

---

## Key Decisions Summary

1. **Same module** (`cmd/nexus-e2e/` or just `go test -tags e2e ./test/e2e/...`) — no separate `packages/nexus-e2e/`
2. **`test/e2e/` replaces `test/bdd/`** — drop the Gherkin/BDD framing; plain Go tests with `t.Run` subtests read better and are easier to maintain
3. **Each test gets its own daemon instance** — no shared state between top-level tests
4. **Harness hides all subprocess + socket details** — test files only call `h.Call()` and assert results
5. **XCUITest is fully optional** — env-var gated, separate CI job, never blocks main e2e suite
