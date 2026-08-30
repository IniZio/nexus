# Why your `--dry-run` flag is treated as a positional argument

## Root cause: `make build` does not install the nexus3 CLI binary

`make build` runs:

```makefile
build:
	$(call CAPPED,go build -p $(GOBUILD_P) ./...)
```

`go build ./...` compiles every package and checks for errors, but **it does not write a `nexus3` binary anywhere on your PATH**. The binary you're running when you type `nexus3 sandbox create` is the previously installed version — the one that predates your change.

Your edit is real. The build succeeded. The binary being invoked is stale.

## How to confirm

```sh
which nexus3
# e.g. /home/you/.local/bin/nexus3 or /usr/local/bin/nexus3

# Check its modification time vs your source edit
stat $(which nexus3)
stat internal/cli/cmd_sandbox.go
```

The binary will be older than your source file.

## Fix

Build and install the CLI binary explicitly:

```sh
go build -o $(which nexus3) ./cmd/nexus3
```

Or wherever you want to put it:

```sh
go build -o ~/.local/bin/nexus3 ./cmd/nexus3
```

Then re-run your test — the flag will work.

## Why the symptom looks like "positional arg" and not an error

This is the second thing that may be confusing you. The parser (`parseSandboxCreateArgs` in `internal/cli/cmd_sandbox.go`) has this `default` branch:

```go
default:
    if len(arg) > 1 && arg[0] == '-' {
        return f, &UsageError{Msg: fmt.Sprintf("sandbox create: unknown flag %q", arg)}
    }
    f.positionals = append(f.positionals, arg)
```

If the stale binary sees `--dry-run` it would return an error ("unknown flag"), not silently treat it as positional. So if you are seeing it silently consumed as a positional, one of these is also true:

1. **You're testing against a binary that's even older** — one built before the `default`-branch error guard was added — and it genuinely falls through to `f.positionals`.
2. **The `case "--dry-run":` was added to the wrong function** — e.g. accidentally in a different verb's parser (list, rm, etc.) rather than in `parseSandboxCreateArgs`. The case compiles, passes the build, but is never reached for `sandbox create`.

Check by grepping:

```sh
grep -n '"--dry-run"' internal/cli/cmd_sandbox.go
```

Make sure the line number falls inside `parseSandboxCreateArgs` (starts around line 584), not inside another function.

## Summary

1. `make build` = compile check only, no binary installed.
2. Install with `go build -o <path-on-your-PATH> ./cmd/nexus3`.
3. Also verify the `case "--dry-run":` is inside `parseSandboxCreateArgs`, not a different verb's parser.
