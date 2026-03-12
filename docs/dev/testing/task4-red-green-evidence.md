# Task 4 Red/Green Evidence

Observed in this session for `TestProjectAndBranchScaffolds`.

## RED

Command:

```bash
go test ./internal/cli -run TestProjectAndBranchScaffolds -v
```

Output:

```text
=== RUN   TestProjectAndBranchScaffolds
    interface_surface_test.go:168: expected project list to define argument validation
--- FAIL: TestProjectAndBranchScaffolds (0.00s)
FAIL
FAIL	github.com/nexus/nexus/packages/nexusd/internal/cli	0.263s
FAIL
```

## GREEN

Command:

```bash
go test ./internal/cli -run TestProjectAndBranchScaffolds -v
```

Output:

```text
=== RUN   TestProjectAndBranchScaffolds
--- PASS: TestProjectAndBranchScaffolds (0.00s)
PASS
ok  	github.com/nexus/nexus/packages/nexusd/internal/cli	0.258s
```
