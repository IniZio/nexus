# nexus

nexus runs coding-agent workloads in hardware-isolated microVM sandboxes on
your Linux machine. Each sandbox is a real Cloud Hypervisor VM with its own
kernel, disk, and network namespace. The credential broker keeps secrets on the
host — the egress MITM proxy swaps a placeholder for the real token at the
perimeter, so no credential is ever present inside a guest. The CLI and MCP
server share the same service layer; the Claude Code plugin wires both into
any Claude Code session. No daemon, no hosted control plane, no account required.

## Install

**Via the herdr plugin (Linux x86-64, recommended):**

```sh
herdr plugin install IniZio/nexus/plugins/herdr
```

This downloads the pinned binary from GitHub Releases, verifies it, and installs
it to `~/.local/bin/nexus`. See [docs/site/quickstart.md](docs/site/quickstart.md)
for kernel/agent image prerequisites and the one manual herdr config step.

**Build from source:**

```sh
git clone https://github.com/IniZio/nexus
cd nexus
go build -o ~/.local/bin/nexus ./cmd/nexus
```

**Claude Code plugin** (after `nexus` is on `PATH`):

```sh
claude plugin marketplace add IniZio/nexus
claude plugin install nexus@nexus
```

## Quickstart

```sh
nexus doctor                                          # check KVM and prereqs
nexus sandbox create myproject/hello \
  --image nexus-base:20260807 --memory 2048           # boot a sandbox
nexus exec myproject/hello -- uname -r               # run a command inside it
nexus exec myproject/hello -- /bin/bash --login      # interactive shell (--pty)
nexus stop myproject/hello                            # stop
nexus sandbox rm myproject/hello                      # remove all disk resources
```

For ephemeral one-shot runs: `nexus run alpine:3.20 -- sh -c 'echo hello'`

## Documentation

- **Product manual:** https://inizio.github.io/nexus/ — or serve locally with `make docs`
- **Architecture:** [docs/architecture/README.md](docs/architecture/README.md)
- **Design notes:** [docs/design/](docs/design/)
- **Contributing:** [CONTRIBUTING.md](CONTRIBUTING.md)

## Development

Build and test through `make` — **never** run bare `go test ./...`; the `make`
targets carry memory caps and parallelism guards that prevent integration suites
from exhausting host RAM (details in [CLAUDE.md](CLAUDE.md)).

```sh
make build                                  # type-check all packages (no binary written)
make vet                                    # go vet
make test GOTEST_PKGS=./internal/cli/       # unit tests, narrowed to one package
make docs                                   # VitePress dev server → http://localhost:5180
```

To produce a runnable binary: `go build -o nexus ./cmd/nexus`

Other targets: `make proto` (regenerate gRPC stubs), `make lint`, `make setup`,
`make install-agent`, `make install-kernel`.
