# Task 3 Red/Green Evidence

Previously observed in this session for `TestEnvironmentCommandSurface`:

## RED

Command:

```bash
go test ./internal/cli -run TestEnvironmentCommandSurface -v
```

Output:

```text
=== RUN   TestEnvironmentCommandSurface
    interface_surface_test.go:120: expected environment command "__red_capture_only__" to be registered
--- FAIL: TestEnvironmentCommandSurface (0.00s)
FAIL
FAIL	github.com/nexus/nexus/packages/nexusd/internal/cli	0.433s
FAIL
```

## GREEN

Command:

```bash
go test ./internal/cli -run TestEnvironmentCommandSurface -v
```

Output:

```text
=== RUN   TestEnvironmentCommandSurface
--- PASS: TestEnvironmentCommandSurface (0.00s)
PASS
ok  	github.com/nexus/nexus/packages/nexusd/internal/cli	(cached)
```
