# Root cause: `make build` does not update the binary in PATH

You're running a stale binary. `make build` in this repo expands to:

```
go build -p $(GOBUILD_P) ./...
```

`go build ./...` with no `-o` flag is a **compile check only** — Go compiles every package and then discards the resulting binaries. It does not write a `nexus3` binary anywhere on disk. Whatever binary is in your PATH is unchanged from the last time it was explicitly installed.

## How to confirm

```bash
which nexus3
nexus3 --version   # or check the mtime of the binary
ls -la $(which nexus3)
```

Compare that timestamp against when you ran `make build`. They'll differ.

## How to fix

Build and install the CLI binary explicitly:

```bash
go build -o ~/.local/bin/nexus3 ./cmd/nexus3
```

Or, if you just want to test locally without touching the installed copy:

```bash
go build -o /tmp/nexus3 ./cmd/nexus3
/tmp/nexus3 sandbox create --dry-run ...
```

## Why the symptom is "treated like a positional"

The parser in `parseSandboxCreateArgs` (`internal/cli/cmd_sandbox.go:584`) is a hand-rolled `for`/`switch` loop — no `flag` package. The current `default` branch (line 817) returns a `UsageError` for any unrecognised flag starting with `-`. An old binary from before that branch was introduced would instead fall through to `f.positionals = append(f.positionals, arg)`, which is exactly the behaviour you're seeing. That's a second confirmation the binary on PATH predates your edits (and possibly predates that guard entirely).

## Checklist once you have the right binary running

1. The new `case "--dry-run":` belongs inside the `switch arg {` block in `parseSandboxCreateArgs` — same level as `case "--rm":`, `case "--force":`, etc.
2. Add a field to `sandboxCreateFlags` and wire it through to `runSandboxCreate`.
3. Rebuild with `-o` as above, then test.
