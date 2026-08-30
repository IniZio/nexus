# Why `make` was missing — and whether it was OK to skip it

## Short answer

No, it was not OK. The agent should have stopped and reported the problem rather than falling back to bare `go build` / `go vet`. The fallback silently breaks the memory-safety rules in CLAUDE.md and produces a different (unguarded) test configuration.

## Root cause

`build-essential` (which provides `make` and `gcc`) was added to `.nexus/Containerfile` only today, in commit `2ff72fc` ("fix(image): install build-essential in the guest image"). The sandbox the agent is running in was built from an earlier image that predates that commit — at that time the Containerfile only installed `ca-certificates`, `curl`, `git`, `iproute2`, `passt`, `virtiofsd`, and `xz-utils`, with no `make` or C toolchain.

The fix is already in the Containerfile. To get a sandbox with `make` the agent needs a freshly built image (i.e. stop the sandbox, let nexus3 rebuild the guest image from the updated Containerfile, and start a new sandbox).

## Why the fallback is not equivalent

CLAUDE.md requires `make build` / `make vet` / `make test` — not as style preference but as a memory-safety rule. The `make` targets add three guards that bare `go` commands omit:

- capped package/test parallelism
- `choom -n 1000` so the kernel OOM-kills this process tree before session infrastructure
- a `systemd-run` scope with `MemoryHigh`/`MemoryMax` so a runaway suite dies inside its own cgroup

`go test -race ./...` inside a VM that is itself consuming locked guest RAM can exhaust host memory and trigger the global OOM killer, which tears down `dbus`, `ssh-agent`, and the entire login session — including any coding-agent session running inside it. That is the exact scenario the `make` guards exist to prevent.

Additionally, `make test` invokes `-race`, which needs `cgo` and therefore a C toolchain. Running `go test` without `-race` produces a structurally different (weaker) test run, so even if the bare commands succeed they do not validate the same things.

## What the agent should do instead

1. Detect `make` is missing.
2. Stop immediately with a clear error: "make is not installed in this sandbox; the guest image predates commit 2ff72fc. Please rebuild the sandbox from the updated Containerfile before proceeding."
3. Not attempt bare `go` commands as a substitute.
