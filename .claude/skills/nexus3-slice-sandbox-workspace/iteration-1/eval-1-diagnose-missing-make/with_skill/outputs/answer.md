# No, it is not OK — and here is why `make` is missing

## Why `make` is missing

The worktree sandbox boots from `images/base/Containerfile`, which **explicitly excludes `gcc`, `build-essential`, and `make`**. The comment in that file explains the reasoning: `CGO_ENABLED=0` throughout nexus3, so a C toolchain is unnecessary for the build itself.

`make` ships inside `build-essential`, so it is absent from the guest for the same reason.

You may be thinking of `.nexus/Containerfile` — that one *does* install `build-essential` (line 22–30 with an explanatory comment). But that file is used for building nexus3 on the *host*, not for the guest VM image the sandbox runs. The guest uses the base image.

## Why bare `go build` / `go vet` is not a safe substitute

Three things break when an agent bypasses `make`:

1. **`-race` is silently off.** The race detector requires CGO, and CGO requires `gcc`. Without `gcc` in the guest, `go test -race` silently degrades to no-race. Data races go undetected.

2. **Memory-safety guards are absent.** The `make` targets cap package and test parallelism (`GOTEST_P`, `GOTEST_PARALLEL`), set `choom -n 1000`, and wrap everything in a `systemd-run` scope with `MemoryHigh`/`MemoryMax`. Bare `go test ./...` runs none of those guards. On a shared host this has historically exhausted RAM and OOM-killed `dbus` and `ssh-agent`, tearing down the whole login session.

3. **The CLAUDE.md rule exists for memory safety, not style.** Skipping it is not a workaround — it is the failure mode the rule was written to prevent.

## What to do

Add `apt-get install -y build-essential` as the **first instruction in the agent brief**, before any build or test step. That installs `make` and `gcc` inside the running VM without requiring an image rebuild. Once installed the agent can run `make build` and `make test` normally with all guards in place.

The brief templates in `references/briefs.md` already include this instruction. If the brief you wrote omitted it, add it — it is one of the constraints that is not discoverable from inside the guest.
