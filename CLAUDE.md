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

### `make build` produces no binary

`make build` runs `go build ./...`, which type-checks every package and
**discards the results**. It never writes a CLI binary. The `./nexus3` file in
the repo root is a leftover from some earlier explicit build and can be
arbitrarily old, so installing it after `make build` silently ships stale code —
your change appears to have no effect and the bug looks like it is in your new
code.

To produce a runnable CLI:

    go build -o nexus3 ./cmd/nexus3

Install it with an atomic rename. A plain `cp` over the live path fails with
`Text file busy`, because running supervisors are executing that binary; the
rename leaves them on their old inode, which is what you want:

    cp nexus3 ~/.local/bin/nexus3.new && mv -f ~/.local/bin/nexus3.new ~/.local/bin/nexus3

`make vet` and `make test` remain the right way to check and test — only the
binary-producing step needs the explicit `-o`.

## Developing nexus3 inside nexus3

nexus3 is developed in its own product: a unit of work gets a git worktree, a
herdr workspace, and a nexus3 VM with an agent running inside it. That workflow —
the herdr verbs, nested virtualisation, guest toolchain, agent briefs, and the
traps peculiar to a self-hosting sandbox — is documented in the
`nexus3-slice-sandbox` skill (`.claude/skills/nexus3-slice-sandbox/`), which is
the authoritative source. Consult it before delegating work to a sandbox or
diagnosing a sandbox that came up wrong, rather than reconstructing the workflow
from the code.

One fact from there is worth stating here because it decides whether the rule
above can be followed at all: **the guest image may not ship `make` or `gcc`.**
When it does not, `make test` is unavailable and `-race` cannot work, since the
race detector needs cgo. Install them (`apt-get install -y build-essential`)
rather than falling back to bare `go` commands — the fallback drops the guards
described above and silently disables the race detector.
