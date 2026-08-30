# nexus3

## Always build and test through `make`

Use `make build`, `make vet`, and `make test`. Do **not** run bare `go build ./...`
or `go test ./...` in this repo.

This is a memory-safety rule, not a style preference. The integration suites boot
real cloud-hypervisor VMs whose guest RAM is memfd-backed and therefore resident
and unswappable, and the builder leases up to 8 concurrent builder VMs. A bare
`go test -race ./...` runs one test binary per package at `GOMAXPROCS` (12 here)
on top of that, which has repeatedly exhausted host RAM and tripped the *global*
OOM killer. That does not fail the test politely — it kills `dbus` and
`ssh-agent`, which tears down the whole login session along with any coding-agent
session running inside it.

The `make` targets carry three guards the bare `go` commands do not: capped
package/test parallelism, `choom -n 1000` so the kernel prefers this process tree
over session infrastructure, and a `systemd-run` scope with `MemoryHigh`/
`MemoryMax` so a runaway suite dies inside its own cgroup. See the comment block
above the `build` target in the Makefile.

Need a single package? Wrap it the same way rather than dropping the guards:

    make test GOTEST_P=1 GOTEST_PARALLEL=1

Have real headroom and want speed? Raise the caps explicitly:

    make test GOTEST_P=6 GOTEST_MEM_MAX=24G
