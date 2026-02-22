# 6. Testing

## 6.1 Testing Pyramid

```
                    ▲
                   /│\
                  / │ \         E2E Tests (5%)
                 /  │  \        - Full user workflows
                /   │   \       - Real Docker/Sprite
               /────┼────\      - Cross-platform
              /     │     \
             /      │      \    Integration Tests (15%)
            /       │       \   - Multi-component
           /        │        \  - Real backends
          /─────────┼─────────\ - Database interactions
         /          │          \
        /           │           \ Unit Tests (80%)
       /            │            \- Pure functions
      /             │             \- Mocked dependencies
     /              │              \- Fast execution
    ────────────────┴────────────────
```

## 6.2 Test Coverage Requirements

| Component | Unit | Integration | E2E | Target Coverage |
|-----------|------|-------------|-----|-----------------|
| Workspace Manager | ✅ | ✅ | ✅ | 90% |
| Provider Interface | ✅ | ✅ | ✅ | 85% |
| Docker Backend | ✅ | ✅ | ✅ | 80% |
| Git Manager | ✅ | ✅ | ✅ | 90% |
| Port Allocator | ✅ | ✅ | ✅ | 95% |
| State Store | ✅ | ✅ | ✅ | 90% |
| WebSocket Daemon | ✅ | ✅ | ✅ | 80% |
| SDK (TypeScript) | ✅ | ✅ | ✅ | 85% |
| CLI | ✅ | ✅ | ✅ | 75% |
| File Sync (Mutagen) | ✅ | ✅ | ✅ | 85% |

## 6.3 Unit Testing

### Port Allocator Unit Test

```go
func TestAllocator_Allocate(t *testing.T) {
    tests := []struct {
        name      string
        workspace string
        service   string
        wantPort  int
        wantErr   bool
    }{
        {
            name:      "first allocation",
            workspace: "ws-1",
            service:   "web",
            wantPort:  32800,
        },
        {
            name:      "same workspace, different service",
            workspace: "ws-1",
            service:   "api",
            wantPort:  32801,
        },
        {
            name:      "different workspace",
            workspace: "ws-2",
            service:   "web",
            wantPort:  32810,
        },
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            a := NewAllocator(32800)
            got, err := a.Allocate(tt.workspace, tt.service)
            
            if tt.wantErr {
                assert.Error(t, err)
                return
            }
            
            assert.NoError(t, err)
            assert.Equal(t, tt.wantPort, got)
        })
    }
}
```

### Mock Provider for Testing

```go
type MockProvider struct {
    mock.Mock
}

func (m *MockProvider) Create(ctx context.Context, spec WorkspaceSpec) (*Workspace, error) {
    args := m.Called(ctx, spec)
    return args.Get(0).(*Workspace), args.Error(1)
}

func (m *MockProvider) Start(ctx context.Context, id string) error {
    args := m.Called(ctx, id)
    return args.Error(0)
}

func (m *MockProvider) Stop(ctx context.Context, id string) error {
    args := m.Called(ctx, id)
    return args.Error(0)
}
```

### State Machine Tests

```go
func TestWorkspaceStateMachine(t *testing.T) {
    tests := []struct {
        name          string
        initialState  WorkspaceStatus
        event         Event
        wantState     WorkspaceStatus
        wantErr       bool
    }{
        {
            name:         "stopped + start = running",
            initialState: StatusStopped,
            event:        EventStart,
            wantState:    StatusRunning,
        },
        {
            name:         "running + stop = stopped",
            initialState: StatusRunning,
            event:        EventStop,
            wantState:    StatusStopped,
        },
        {
            name:         "pending + stop = error",
            initialState: StatusPending,
            event:        EventStop,
            wantErr:      true,
        },
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            sm := NewStateMachine(tt.initialState)
            err := sm.Transition(tt.event)
            
            if tt.wantErr {
                assert.Error(t, err)
                return
            }
            
            assert.NoError(t, err)
            assert.Equal(t, tt.wantState, sm.Current())
        })
    }
}
```

## 6.4 Integration Testing

### Docker Provider Integration

```go
func TestDockerProvider_Integration(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping integration test")
    }
    
    ctx := context.Background()
    provider, err := docker.NewProvider()
    require.NoError(t, err)
    defer provider.Close()
    
    // Create workspace
    spec := WorkspaceSpec{
        Name: "test-integration",
        Image: "alpine:latest",
        Resources: ResourceAllocation{
            CPU: 1,
            Memory: 512 * 1024 * 1024,
        },
    }
    
    ws, err := provider.Create(ctx, spec)
    require.NoError(t, err)
    defer provider.Destroy(ctx, ws.ID)
    
    // Start
    err = provider.Start(ctx, ws.ID)
    require.NoError(t, err)
    
    // Verify running
    ws, err = provider.Get(ctx, ws.ID)
    require.NoError(t, err)
    assert.Equal(t, StatusRunning, ws.Status)
    
    // Stop
    err = provider.Stop(ctx, ws.ID)
    require.NoError(t, err)
    
    // Verify stopped
    ws, err = provider.Get(ctx, ws.ID)
    require.NoError(t, err)
    assert.Equal(t, StatusStopped, ws.Status)
}
```

### Git Worktree Integration

```go
func TestGitManager_WorktreeIntegration(t *testing.T) {
    // Setup temp repo
    repo := setupTempRepo(t)
    gm := git.NewManagerWithRepoRoot(repo)
    
    // Create worktree
    path, err := gm.CreateWorktree("feature-test")
    require.NoError(t, err)
    
    // Verify branch created
    branch, err := gm.GetBranch("feature-test")
    require.NoError(t, err)
    assert.Equal(t, "nexus/feature-test", branch)
    
    // Verify worktree directory
    _, err = os.Stat(path)
    require.NoError(t, err)
    
    // Cleanup
    err = gm.RemoveWorktree("feature-test")
    require.NoError(t, err)
}
```

## 6.5 E2E Testing

### Workspace Lifecycle E2E

```typescript
describe('Workspace Lifecycle', () => {
  const testWorkspace = `e2e-test-${Date.now()}`;
  
  afterAll(async () => {
    await cli.run(`workspace destroy ${testWorkspace} --force`);
  });
  
  test('create workspace', async () => {
    const result = await cli.run(`workspace create ${testWorkspace}`);
    expect(result.exitCode).toBe(0);
    expect(result.stdout).toContain('created successfully');
  });
  
  test('list includes new workspace', async () => {
    const result = await cli.run('workspace list');
    expect(result.stdout).toContain(testWorkspace);
  });
  
  test('start workspace', async () => {
    const result = await cli.run(`workspace up ${testWorkspace}`);
    expect(result.exitCode).toBe(0);
  });
  
  test('execute command in workspace', async () => {
    const result = await cli.run(
      `workspace exec ${testWorkspace} echo hello`
    );
    expect(result.stdout).toContain('hello');
  });
  
  test('stop workspace', async () => {
    const result = await cli.run(`workspace down ${testWorkspace}`);
    expect(result.exitCode).toBe(0);
  });
});
```

### Performance E2E Test

```typescript
describe('Performance Requirements', () => {
  test('workspace switch < 2 seconds', async () => {
    // Setup two workspaces
    await cli.run('workspace create perf-test-1');
    await cli.run('workspace create perf-test-2');
    
    // Start both
    await cli.run('workspace up perf-test-1');
    await cli.run('workspace up perf-test-2');
    
    // Measure switch time
    const start = performance.now();
    await cli.run('workspace switch perf-test-1');
    const duration = performance.now() - start;
    
    expect(duration).toBeLessThan(2000);
    
    // Cleanup
    await cli.run('workspace destroy perf-test-1 --force');
    await cli.run('workspace destroy perf-test-2 --force');
  });
});
```

## 6.6 File Sync Testing

### Unit Tests (Mock Mutagen)

```go
func TestSyncManager_CreateSession(t *testing.T) {
    tests := []struct {
        name      string
        workspace string
        hostPath  string
        wantErr   bool
    }{
        {
            name:      "valid session",
            workspace: "test-ws",
            hostPath:  "/tmp/test-worktree",
            wantErr:   false,
        },
        {
            name:      "duplicate session",
            workspace: "existing-ws",
            hostPath:  "/tmp/existing",
            wantErr:   true,
        },
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            mockProvider := &MockSyncProvider{}
            sm := NewSyncManager(mockProvider)
            
            session, err := sm.CreateSession(tt.workspace, tt.hostPath, "/workspace")
            
            if tt.wantErr {
                assert.Error(t, err)
                return
            }
            
            assert.NoError(t, err)
            assert.NotNil(t, session)
            assert.Equal(t, tt.workspace, session.WorkspaceID)
        })
    }
}

func TestSyncManager_Lifecycle(t *testing.T) {
    mockProvider := &MockSyncProvider{}
    sm := NewSyncManager(mockProvider)
    
    // Create
    session, err := sm.CreateSession("test", "/host", "/container")
    require.NoError(t, err)
    
    // Pause
    err = sm.PauseSession("test")
    require.NoError(t, err)
    assert.Equal(t, SyncStatePaused, session.Status)
    
    // Resume
    err = sm.ResumeSession("test")
    require.NoError(t, err)
    assert.Equal(t, SyncStateSyncing, session.Status)
    
    // Terminate
    err = sm.TerminateSession("test")
    require.NoError(t, err)
    
    _, err = sm.GetStatus("test")
    assert.Error(t, err) // Session should not exist
}
```

### Integration Tests (Real Mutagen)

```go
func TestMutagenIntegration_BidirectionalSync(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping integration test")
    }
    
    // Setup temp directories
    hostDir := t.TempDir()
    containerDir := t.TempDir()
    
    // Create Mutagen provider
    provider, err := mutagen.NewProvider()
    require.NoError(t, err)
    
    // Create sync session
    session, err := provider.CreateSession(hostDir, containerDir, SyncConfig{
        Mode: "two-way-safe",
        Exclude: []string{".git", "node_modules"},
    })
    require.NoError(t, err)
    defer provider.Terminate(session.ID)
    
    // Test: Host → Container sync
    hostFile := filepath.Join(hostDir, "test.txt")
    err = os.WriteFile(hostFile, []byte("from host"), 0644)
    require.NoError(t, err)
    
    // Wait for sync
    time.Sleep(500 * time.Millisecond)
    
    // Verify container received file
    containerFile := filepath.Join(containerDir, "test.txt")
    content, err := os.ReadFile(containerFile)
    require.NoError(t, err)
    assert.Equal(t, "from host", string(content))
    
    // Test: Container → Host sync
    err = os.WriteFile(containerFile, []byte("from container"), 0644)
    require.NoError(t, err)
    
    // Wait for sync
    time.Sleep(500 * time.Millisecond)
    
    // Verify host received update
    content, err = os.ReadFile(hostFile)
    require.NoError(t, err)
    assert.Equal(t, "from container", string(content))
}

func TestMutagenIntegration_ConflictResolution(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping integration test")
    }
    
    hostDir := t.TempDir()
    containerDir := t.TempDir()
    
    provider, err := mutagen.NewProvider()
    require.NoError(t, err)
    
    session, err := provider.CreateSession(hostDir, containerDir, SyncConfig{
        Mode:     "two-way-safe",
        Conflict: ConflictConfig{Strategy: "host-wins"},
    })
    require.NoError(t, err)
    defer provider.Terminate(session.ID)
    
    // Create initial file
    testFile := filepath.Join(hostDir, "conflict.txt")
    err = os.WriteFile(testFile, []byte("initial"), 0644)
    require.NoError(t, err)
    time.Sleep(200 * time.Millisecond)
    
    // Pause sync
    provider.Pause(session.ID)
    
    // Modify both sides while paused
    err = os.WriteFile(testFile, []byte("host version"), 0644)
    require.NoError(t, err)
    
    containerFile := filepath.Join(containerDir, "conflict.txt")
    err = os.WriteFile(containerFile, []byte("container version"), 0644)
    require.NoError(t, err)
    
    // Resume sync - should resolve with host winning
    provider.Resume(session.ID)
    time.Sleep(500 * time.Millisecond)
    
    // Verify host won
    content, err := os.ReadFile(containerFile)
    require.NoError(t, err)
    assert.Equal(t, "host version", string(content))
}
```

### Performance Tests

```go
func BenchmarkSync_LargeRepository(b *testing.B) {
    // Create large directory structure
    hostDir := createLargeRepo(b, 50000) // 50k files
    containerDir := b.TempDir()
    
    provider, _ := mutagen.NewProvider()
    
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        session, _ := provider.CreateSession(hostDir, containerDir, SyncConfig{})
        provider.Terminate(session.ID)
    }
}

func TestSyncPerformance_LatencyRequirements(t *testing.T) {
    hostDir := t.TempDir()
    containerDir := t.TempDir()
    
    provider, _ := mutagen.NewProvider()
    session, _ := provider.CreateSession(hostDir, containerDir, SyncConfig{})
    defer provider.Terminate(session.ID)
    
    // Measure sync latency for small file
    start := time.Now()
    os.WriteFile(filepath.Join(hostDir, "latency-test.txt"), []byte("test"), 0644)
    
    // Poll until file appears
    for time.Since(start) < 5*time.Second {
        if _, err := os.Stat(filepath.Join(containerDir, "latency-test.txt")); err == nil {
            break
        }
        time.Sleep(10 * time.Millisecond)
    }
    
    latency := time.Since(start)
    assert.Less(t, latency, 500*time.Millisecond, "Sync latency should be <500ms")
}
```

### E2E File Sync Tests

```typescript
describe('File Sync E2E', () => {
  const testWorkspace = `sync-test-${Date.now()}`;
  
  afterAll(async () => {
    await cli.run(`workspace destroy ${testWorkspace} --force`);
  });
  
  test('create workspace with sync', async () => {
    const result = await cli.run(`workspace create ${testWorkspace}`);
    expect(result.exitCode).toBe(0);
    
    // Verify sync is active
    const status = await cli.run(`workspace sync-status ${testWorkspace}`);
    expect(status.stdout).toContain('syncing');
  });
  
  test('file changes propagate to container', async () => {
    // Create file in worktree
    const testFile = path.join(
      process.env.HOME,
      'projects/myapp/.worktree',
      testWorkspace,
      'sync-test.txt'
    );
    fs.writeFileSync(testFile, 'hello from host');
    
    // Wait for sync
    await new Promise(r => setTimeout(r, 1000));
    
    // Verify in container
    const result = await cli.run(
      `workspace exec ${testWorkspace} cat /workspace/sync-test.txt`
    );
    expect(result.stdout).toContain('hello from host');
  });
  
  test('sync pause and resume', async () => {
    // Pause
    let result = await cli.run(`workspace sync-pause ${testWorkspace}`);
    expect(result.exitCode).toBe(0);
    
    let status = await cli.run(`workspace sync-status ${testWorkspace}`);
    expect(status.stdout).toContain('paused');
    
    // Resume
    result = await cli.run(`workspace sync-resume ${testWorkspace}`);
    expect(result.exitCode).toBe(0);
    
    status = await cli.run(`workspace sync-status ${testWorkspace}`);
    expect(status.stdout).toContain('syncing');
  });
});
```

## 6.7 Real-World Testing

### hanlun-lms Test Scenario

```yaml
Project: hanlun-lms
Repository: git@github.com:oursky/hanlun-lms.git
Type: Learning Management System
Stack:
  Frontend: Next.js 14, TypeScript, Tailwind CSS
  Backend: Node.js, Express, tRPC
  Database: PostgreSQL 15, Redis
  Infrastructure: Docker Compose
Complexity:
  Services: 6 (web, api, db, redis, worker, nginx)
  Build time: ~3 minutes (cold)
  Startup time: ~30 seconds
  Port requirements: 3000, 3001, 5432, 6379, 8080
```

### Parallel Development Test

```bash
# === Test Procedure ===

# 1. Create two workspaces
boulder workspace create alice-dashboard --template=node-postgres
boulder workspace create bob-api --template=node-postgres

# 2. Both workspaces should have:
#    - Isolated git branches (nexus/alice-dashboard, nexus/bob-api)
#    - Isolated directories (.worktree/)
#    - Isolated containers
#    - Isolated ports (32800-32809 for alice, 32810-32819 for bob)

# 3. Start both workspaces
boulder workspace switch alice-dashboard
npm run dev  # Accessible on localhost:32801

boulder workspace switch bob-api
npm run dev  # Accessible on localhost:32811

# 4. Context switch test
time boulder workspace switch alice-dashboard
# Should complete in <2 seconds
```

### Success Criteria

| Criterion | Requirement | Measurement |
|-----------|-------------|-------------|
| **Parallel operation** | Both workspaces run simultaneously | Verify 6 containers each |
| **No port conflicts** | All services accessible | curl all endpoints |
| **Sub-2s switch** | Context switch < 2 seconds | `time boulder workspace switch` |
| **State preservation** | Dev server continues after switch | Verify hot reload works |
| **Git isolation** | No merge conflicts on switch | `git status` shows clean |
| **Data persistence** | Database survives restart | Write data, restart, verify |

## 6.8 Chaos Testing

```go
func TestChaos_RandomFailures(t *testing.T) {
    ctx := context.Background()
    fi := chaos.NewFaultInjector()
    
    for i := 0; i < 100; i++ {
        // Randomly inject failures
        fi.InjectRandomFaults([]chaos.FaultType{
            chaos.NetworkLatency,
            chaos.DiskFull,
            chaos.ContainerCrash,
            chaos.PortConflict,
            chaos.SyncInterruption,
            chaos.DiskCorruption,
        })
        
        // Run operation
        err := workspaceManager.Create(ctx, fmt.Sprintf("chaos-%d", i))
        
        // Verify graceful handling
        assert.True(t, err == nil || isRecoverable(err))
        
        fi.Reset()
    }
}

func TestChaos_SyncFailureRecovery(t *testing.T) {
    ctx := context.Background()
    
    // Create workspace with sync
    ws, err := manager.Create("sync-chaos-test")
    require.NoError(t, err)
    
    // Simulate sync daemon crash
    chaos.KillProcess("mutagen-daemon")
    
    // Wait for detection
    time.Sleep(2 * time.Second)
    
    // Verify workspace still functional
    ws, err = manager.Get("sync-chaos-test")
    require.NoError(t, err)
    assert.Equal(t, StatusRunning, ws.Status)
    
    // Verify sync recovered
    status, err := manager.GetSyncStatus("sync-chaos-test")
    require.NoError(t, err)
    assert.Equal(t, SyncStateSyncing, status.State)
}

func TestRecovery_FromCrash(t *testing.T) {
    // Create workspace
    ws, _ := manager.Create("recovery-test")
    
    // Simulate crash mid-operation
    simulateCrash()
    
    // Verify recovery on restart
    manager2 := NewManager()
    
    ws, err := manager2.Get("recovery-test")
    require.NoError(t, err)
    
    // Should be able to repair
    err = manager2.Repair("recovery-test")
    require.NoError(t, err)
    
    // Should be usable again
    err = manager2.Start("recovery-test")
    require.NoError(t, err)
}

func TestSyncRecovery_FromInterruption(t *testing.T) {
    // Create workspace
    ws, _ := manager.Create("sync-recovery-test")
    require.NoError(t, err)
    
    // Pause sync
    err = manager.PauseSync("sync-recovery-test")
    require.NoError(t, err)
    
    // Simulate crash
    simulateCrash()
    
    // Restart manager
    manager2 := NewManager()
    
    // Should detect paused sync and offer to resume
    status, err := manager2.GetSyncStatus("sync-recovery-test")
    require.NoError(t, err)
    assert.Equal(t, SyncStatePaused, status.State)
    
    // Resume should work
    err = manager2.ResumeSync("sync-recovery-test")
    require.NoError(t, err)
}

## 6.9 Performance Benchmarks

### Target Metrics

| Metric | Target | Acceptable | Measurement |
|--------|--------|------------|-------------|
| **Cold start** | <30s | <60s | Time from create to ready |
| **Warm start** | <2s | <5s | Time from stop to running |
| **Context switch** | <2s | <5s | Time to switch between workspaces |
| **File read (1MB)** | <100ms | <500ms | fs.readFile latency |
| **File write (1MB)** | <200ms | <1s | fs.writeFile latency |
| **Exec command** | <500ms | <2s | Simple command execution |
| **List workspaces** | <100ms | <500ms | boulder workspace list |
| **Port allocation** | <50ms | <200ms | Assign new port |
| **Snapshot create** | <5s | <15s | Checkpoint workspace |
| **Snapshot restore** | <10s | <30s | Restore from checkpoint |
| **Sync latency** | <500ms | <2s | File change propagation |
| **Initial sync (10k files)** | <10s | <30s | First-time sync duration |
| **Sync throughput** | >10MB/s | >5MB/s | Large file transfer rate |

### Benchmark Implementation

```go
func BenchmarkWorkspaceLifecycle(b *testing.B) {
    ctx := context.Background()
    provider := setupBenchmarkProvider(b)
    
    b.Run("Create", func(b *testing.B) {
        for i := 0; i < b.N; i++ {
            name := fmt.Sprintf("bench-create-%d", i)
            _, err := provider.Create(ctx, WorkspaceSpec{Name: name})
            if err != nil {
                b.Fatal(err)
            }
        }
    })
    
    b.Run("StartStop", func(b *testing.B) {
        ws, _ := provider.Create(ctx, WorkspaceSpec{Name: "bench-startstop"})
        
        b.ResetTimer()
        for i := 0; i < b.N; i++ {
            provider.Start(ctx, ws.ID)
            provider.Stop(ctx, ws.ID)
        }
    })
    
    b.Run("Switch", func(b *testing.B) {
        ws1, _ := provider.Create(ctx, WorkspaceSpec{Name: "bench-ws1"})
        ws2, _ := provider.Create(ctx, WorkspaceSpec{Name: "bench-ws2"})
        
        b.ResetTimer()
        for i := 0; i < b.N; i++ {
            if i%2 == 0 {
                provider.Start(ctx, ws1.ID)
                provider.Stop(ctx, ws2.ID)
            } else {
                provider.Start(ctx, ws2.ID)
                provider.Stop(ctx, ws1.ID)
            }
        }
    })
    
    b.Run("SyncLatency", func(b *testing.B) {
        ws, _ := provider.Create(ctx, WorkspaceSpec{Name: "bench-sync"})
        worktreePath := ws.WorktreePath
        
        b.ResetTimer()
        for i := 0; i < b.N; i++ {
            // Write file in worktree
            testFile := filepath.Join(worktreePath, fmt.Sprintf("test-%d.txt", i))
            os.WriteFile(testFile, []byte("test content"), 0644)
            
            // Wait for sync (poll with timeout)
            deadline := time.Now().Add(5 * time.Second)
            for time.Now().Before(deadline) {
                status, _ := provider.GetSyncStatus(ws.ID)
                if status.LastSyncAt.After(time.Now().Add(-time.Second)) {
                    break
                }
                time.Sleep(10 * time.Millisecond)
            }
        }
    })
}

## 6.10 Test Infrastructure

```yaml
# Test configuration
# .nexus/test-config.yaml

environments:
  unit:
    backend: mock
    parallel: true
    coverage: true
    
  integration:
    backends:
      - docker
      - mock
    requires:
      - docker
    timeout: 5m
    
  e2e:
    backends:
      - docker
    matrix:
      os: [ubuntu, macos, windows]
      docker_version: [24.0, 25.0]
    parallel: false
    timeout: 30m

fixtures:
  repositories:
    - name: node-app
      url: https://github.com/example/node-app
    - name: go-app
      url: https://github.com/example/go-app
```
