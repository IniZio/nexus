# Diagnosis: stale binary from `make build`

The flag is parsed correctly. The binary running when you test is not the binary `make build` produced.

**Root cause:** `make build` runs `go build -p N ./...`. Passing `./...` tells the Go toolchain to build every package, which it type-checks and **discards**. No output binary is written. The `./nexus3` file sitting in the repo root is a leftover from some previous explicit build and can be arbitrarily old. When you copy or install that file after `make build`, you ship stale code — your new flag never reaches the running parser.

This is documented in `references/gotchas.md` under "Stale binary".

**Fix:**

```bash
go build -o nexus3 ./cmd/nexus3
```

Then install with an atomic rename to avoid "Text file busy" on any running supervisor that has the old binary open:

```bash
cp nexus3 ~/.local/bin/nexus3.new && mv -f ~/.local/bin/nexus3.new ~/.local/bin/nexus3
```

The rename is important: a plain `cp` over the live path fails when a supervisor is running, because the supervisor holds the old inode open. The rename lets running supervisors keep their old inode while new invocations pick up the new binary.

After that, re-test. The case statement you've been re-reading is almost certainly fine.

(`make vet` and `make test` remain the right way to check and run tests — only the binary-producing step needs the explicit `-o` invocation.)
