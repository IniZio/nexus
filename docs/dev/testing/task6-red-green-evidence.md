# Task 6 Red-Green Evidence

## TestLegacyRootCommandsRemoved (Red)

Command:

```bash
go test ./internal/cli -run TestLegacyRootCommandsRemoved -v
```

Observed output:

```text
=== RUN   TestLegacyRootCommandsRemoved
=== RUN   TestLegacyRootCommandsRemoved/workspace_not_registered_on_root
=== RUN   TestLegacyRootCommandsRemoved/trace_not_registered_on_root
=== RUN   TestLegacyRootCommandsRemoved/environment_not_registered_on_root
    legacy_root_commands_test.go:16: expected root command "environment" to not be registered
--- FAIL: TestLegacyRootCommandsRemoved (0.00s)
    --- PASS: TestLegacyRootCommandsRemoved/workspace_not_registered_on_root (0.00s)
    --- PASS: TestLegacyRootCommandsRemoved/trace_not_registered_on_root (0.00s)
    --- FAIL: TestLegacyRootCommandsRemoved/environment_not_registered_on_root (0.00s)
FAIL
FAIL	github.com/nexus/nexus/packages/nexusd/internal/cli	0.363s
FAIL
```

## TestLegacyRootCommandsRemoved (Green)

Command:

```bash
go test ./internal/cli -run TestLegacyRootCommandsRemoved -v
```

Observed output:

```text
=== RUN   TestLegacyRootCommandsRemoved
=== RUN   TestLegacyRootCommandsRemoved/workspace_not_registered_on_root
=== RUN   TestLegacyRootCommandsRemoved/trace_not_registered_on_root
--- PASS: TestLegacyRootCommandsRemoved (0.00s)
    --- PASS: TestLegacyRootCommandsRemoved/workspace_not_registered_on_root (0.00s)
    --- PASS: TestLegacyRootCommandsRemoved/trace_not_registered_on_root (0.00s)
PASS
ok  	github.com/nexus/nexus/packages/nexusd/internal/cli	0.527s
```
