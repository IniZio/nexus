# Task 5 Red-Green Evidence

## TestTraceRehomedUnderTargetModel (Red)

Command:

```bash
go test ./internal/cli -run TestTraceRehomedUnderTargetModel -v
```

Observed output:

```text
=== RUN   TestTraceRehomedUnderTargetModel
    interface_surface_test.go:165: expected version history to expose trace command
--- FAIL: TestTraceRehomedUnderTargetModel (0.00s)
FAIL
FAIL	github.com/nexus/nexus/packages/nexusd/internal/cli	0.494s
FAIL
```

## TestTraceRehomedUnderTargetModel (Green)

Command:

```bash
go test ./internal/cli -run TestTraceRehomedUnderTargetModel -v
```

Observed output:

```text
=== RUN   TestTraceRehomedUnderTargetModel
--- PASS: TestTraceRehomedUnderTargetModel (0.00s)
PASS
ok  	github.com/nexus/nexus/packages/nexusd/internal/cli	0.176s
```
