# Why `make` was missing and whether bare `go` commands are OK

## Short answer: not OK, and it was a real gap in the image

The agent was right that `make` was missing — but wrong to proceed with bare `go build` / `go vet`. The sandbox was running a guest image built **before** `build-essential` was added to `.nexus/Containerfile`.

## What happened

The `.nexus/Containerfile` did not install `build-essential` (which provides `make` and `gcc`) until commit `2ff72fc` landed today (2026-08-30). Any sandbox whose guest image was built from an earlier version of that file — which means any sandbox created before today — is missing `make` entirely.

The fix is already committed. The comment block added with that commit explains it explicitly:

> `build-essential: make + gcc. CLAUDE.md requires this repo be built and tested through make build / make vet / make test rather than bare go commands, and make test runs -race, which needs cgo and therefore a C toolchain. Without these an in-guest agent cannot follow the repo's own build rule and silently falls back to bare go commands — or apt-installs them itself, which is how two sandboxes built from this same file ended up with different toolchains and validated against different test configurations.`

## Why bare `go` commands are not a safe fallback here

Three concrete problems:

1. **`-race` is silently dropped.** `make test` passes `-race` to go test. Bare `go test` without that flag misses data races — including the ones this repo's CI is guarding. A green bare-go run does not mean what a green `make test` means.

2. **Memory guards are gone.** The `make` targets wrap tests in `systemd-run` with `MemoryHigh`/`MemoryMax` and `choom -n 1000`. Bare `go test` has none of that. In a sandbox running real CH VMs this is a real OOM risk (it has killed login sessions before).

3. **Parallelism caps are gone.** `make test` caps package and test parallelism (`GOTEST_P`, `GOTEST_PARALLEL`). Bare `go test ./...` runs one binary per package at `GOMAXPROCS`, which can exhaust guest RAM.

## What to do with that sandbox now

The sandbox image needs to be rebuilt from the updated Containerfile. The quickest fix is to destroy and recreate the sandbox so it picks up the new image. Alternatively, inside the existing sandbox you can run:

```
sudo apt-get update && sudo apt-get install -y build-essential
```

to unblock the current session — but that is a temporary patch; a fresh sandbox from the new image is cleaner and matches what every future sandbox will have.

## Bottom line

The agent should not have proceeded with bare `go` commands. It should have reported the missing `make` as a blocker and asked for a rebuilt sandbox. The Containerfile gap is now fixed; any sandbox created after today will have `make` available out of the box.
