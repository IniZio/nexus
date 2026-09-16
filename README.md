# nexus3

MicroVM sandboxes for agentic parallel development — each task gets its own
isolated Linux kernel.

nexus3 runs workloads in microVM sandboxes: real Linux VMs with their own
kernel, disk image, network namespace, and an in-guest agent reachable over
vsock. The CLI and the MCP server expose the same primitives, so an
orchestrator drives nexus3 the same way a human does. Credentials stay on
the host; the MITM egress proxy swaps placeholders for real secrets at the
perimeter — no token is ever present inside a guest. Works on Linux with
KVM; no daemon, no hosted control plane, no account required.

## Stack

- **Go 1.25.5** — single module (`github.com/IniZio/nexus3`)
- **cloud-hypervisor** — VMM; each sandbox is a separate CH process
- **gRPC / protobuf over vsock** — host↔guest control plane
  (`proto/nexus3/agent/v1/agent.proto`)
- **virtiofs** — live host-path mounts into running guests
- **herdr** — workspace/pane lifecycle integration (`plugins/herdr/`)
- **Orca** — recipe-driven sandbox provisioning (`plugins/orca/`)

## Install

**From a GitHub release** (recommended):

```sh
# Download the binary for your arch from the latest GitHub release,
# then install with an atomic rename so running supervisors are unaffected:
cp nexus3 ~/.local/bin/nexus3.new
mv -f ~/.local/bin/nexus3.new ~/.local/bin/nexus3
```

**Build from source:**

```sh
go build -o nexus3 ./cmd/nexus3
cp nexus3 ~/.local/bin/nexus3.new
mv -f ~/.local/bin/nexus3.new ~/.local/bin/nexus3
```

## Quick start

```sh
nexus3 create my-sandbox            # boot a sandbox
nexus3 exec my-sandbox -- uname -a  # run a command inside it
nexus3 ps                           # list running sandboxes
nexus3 rm my-sandbox                # destroy it
```

## Integrations

| Integration | Path | Notes |
|---|---|---|
| **Claude Code plugin** | `plugins/claude/` | Install via `.claude-plugin/marketplace.json`; exposes the MCP server and skill namespace |
| **herdr plugin** | `plugins/herdr/` | Builds + registers the herdr plugin so panes bind to sandbox VMs |
| **Orca recipe** | `plugins/orca/recipes/nexus3.json` | `nexus3 orca create/suspend/resume/destroy` |

## Development

Build, vet, and test through `make` — **never** run bare `go test ./...`;
the `make` targets carry memory caps and parallelism guards that prevent the
integration suites from exhausting host RAM (see `CLAUDE.md`).

```sh
make build          # type-check all packages (no binary written)
make vet            # go vet
make test           # unit tests (capped parallelism, choom guard)
make docs           # docs dev server → http://localhost:5180
make proto          # regenerate gRPC stubs from proto/nexus3/agent/v1/agent.proto
```

To produce a runnable binary: `go build -o nexus3 ./cmd/nexus3`

## Testing tiers

| Tier | Command | Requirements |
|---|---|---|
| Unit | `make test` | none |
| Integration | `make test-integration` | `/dev/kvm` |
| herdr live | `make test-herdr-live` | `/dev/kvm` + herdr |

## Docs

- **Product manual:** `docs/site/` (`make docs` to serve locally)
- **Design notes:** `docs/design/`
- **Contributing:** `CONTRIBUTING.md`
