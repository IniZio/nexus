# Architecture

nexus runs workloads in microVM sandboxes — real Linux VMs with their own kernel, disk image,
and network namespace. A detached supervisor process owns each sandbox's lifetime; a gRPC agent
inside the guest (reachable over vsock) accepts exec/copy/resize commands from the host CLI or
MCP server. Credentials never enter the guest: the MITM egress proxy on the host swaps
placeholder tokens for real secrets at the perimeter.

## C4 Context diagram

```mermaid
C4Context
  title nexus — System Context

  Person(dev, "Developer / Orchestrator", "Runs nexus CLI or connects via MCP")

  System(nexus, "nexus", "MicroVM sandbox runtime — creates isolated Linux VMs, routes egress, brokers credentials")

  System_Ext(herdr, "herdr", "Workspace/pane lifecycle manager; opens terminal panes and dispatches agent briefs")
  System_Ext(kvm, "KVM + cloud-hypervisor", "Linux KVM hypervisor; cloud-hypervisor binary owns each VM process")
  System_Ext(claude, "Claude Code agent", "AI coding agent running inside the guest VM")
  System_Ext(upstream, "GitHub / upstream internet", "Package registries, GitHub API, LLM endpoints — egress filtered by nexus perimeter")

  Rel(dev, nexus, "sandbox create / exec / snapshot / fork", "CLI flags or MCP JSON-RPC")
  Rel(nexus, herdr, "opens panes, dispatches briefs", "herdr CLI sub-commands")
  Rel(nexus, kvm, "spawns cloud-hypervisor process per sandbox", "exec + KVM fd")
  Rel(claude, nexus, "gRPC over vsock (exec, copy, resize)", "virtio-vsock")
  Rel(nexus, upstream, "proxied egress; blocked by allowlist", "MITM HTTPS proxy")
  Rel(dev, herdr, "opens workspaces, views panes", "herdr CLI / UI")
  Rel(herdr, nexus, "herdr plugin calls nexus MCP server", "MCP stdio")
```

## C4 Container diagram

```mermaid
C4Container
  title nexus — Containers

  Person(dev, "Developer / Orchestrator")

  Container(cli, "nexus CLI", "Go binary — cmd/nexus", "Parses flags, calls Service, drives lifecycle")
  Container(mcp, "MCP Server", "Go — internal/mcp + internal/cli", "JSON-RPC over stdio; herdr plugin entry point (plugins/claude)")
  Container(svc, "Core Service", "Go — internal/core/service", "Orchestrates sandbox create/start/stop/fork; owns store and volume lifecycle")
  Container(store, "Sandbox Store", "Go — internal/core/store", "Durable record of sandbox state; keyed by SandboxID")
  Container(volstore, "Volume Store", "Go — internal/core/volumestore", "Named-volume lifecycle; CoW disk leases (SD2)")
  Container(imgstore, "Image Store", "Go — internal/core/image", "Resolves and caches base disk images")
  Container(driver, "CH Driver", "Go — internal/core/driver/cloudhypervisor", "Wraps cloud-hypervisor REST API; manages VM config, disks, virtiofs tags, vsock")
  Container(supervisor, "Detached Supervisor", "Go — internal/supervisor (re-exec of nexus binary)", "One process per sandbox; owns cloud-hypervisor child, perimeter, watchdog pipe")
  Container(perimeter, "Egress Perimeter", "Go — internal/core/perimeter (+mitm, netfilter, sni)", "Frame pump on TAP fd; netfilter AllowList (L3/L4) + MITM HTTPS proxy (L7)")
  Container(agent, "Guest Agent", "Go — cmd/nexus-agent (static binary in guest)", "PID-1-ish in VM; gRPC/vsock server; exec/copy/resize/hotswap")

  System_Ext(kvm, "cloud-hypervisor binary", "KVM VMM process")
  System_Ext(upstream, "GitHub / upstream internet")
  System_Ext(herdr, "herdr")

  Rel(dev, cli, "nexus sandbox create / exec / …", "stdin/stdout")
  Rel(dev, mcp, "MCP tool calls", "JSON-RPC stdio")
  Rel(herdr, mcp, "herdr plugin: space-create, space-agent, …", "MCP stdio")
  Rel(cli, svc, "Create / Start / Stop / Fork / Remove", "Go function call")
  Rel(mcp, svc, "same Service interface", "Go function call")
  Rel(svc, store, "persist sandbox record", "Go function call")
  Rel(svc, volstore, "attach/detach named volumes", "Go function call")
  Rel(svc, imgstore, "resolve base image path", "Go function call")
  Rel(svc, supervisor, "SpawnDetached / SpawnAdoptDetached", "exec + Unix socket ready-signal")
  Rel(supervisor, driver, "Start / Stop / Resize via CH REST API", "HTTP localhost")
  Rel(supervisor, kvm, "spawns as child process", "exec")
  Rel(supervisor, perimeter, "Start(fd, proxy, allowList)", "Go function call in-process")
  Rel(perimeter, upstream, "allow-listed HTTPS; blocks everything else", "MITM proxy + netfilter")
  Rel(agent, svc, "vsock gRPC: Exec / Copy / AgentInfo / RestartAgent", "virtio-vsock CID")
  Rel(svc, agent, "dials vsock to run commands in guest", "virtio-vsock")
```

## Container → code mapping

| Container | Primary packages |
|-----------|-----------------|
| nexus CLI | `cmd/nexus`, `internal/cli` |
| MCP Server | `internal/mcp`, `internal/cli` (shared `Service`); plugin: `plugins/claude` |
| Core Service | `internal/core/service` |
| Sandbox Store | `internal/core/store` |
| Volume Store | `internal/core/volumestore` |
| Image Store | `internal/core/image` |
| CH Driver | `internal/core/driver/cloudhypervisor` |
| Detached Supervisor | `internal/supervisor` (re-exec of `cmd/nexus` binary with `--supervisor` flag) |
| Egress Perimeter | `internal/core/perimeter`, `internal/core/perimeter/mitm`, `internal/core/perimeter/netfilter`, `internal/core/perimeter/sni` |
| Guest Agent | `cmd/nexus-agent`, `internal/core/agent/agentpb` (gRPC proto), `internal/clientagent` |

## Request flow: `nexus sandbox create`

1. **CLI** (`internal/cli/cmd_sandbox.go`) parses flags, resolves `nexus.yaml` / `.nexus/config.yaml`,
   calls `svc.Create` then `svc.Boot`.
2. **Core Service** (`internal/core/service/create.go`) mints a `domain.SandboxID`, persists the
   record via **Sandbox Store**, attaches named-volume leases via **Volume Store**, resolves the
   base disk image via **Image Store**.
3. **Service** calls `supervisor.SpawnDetached` (`internal/supervisor/spawn_linux.go`), which
   re-execs the nexus binary in a new session; the child writes a pidfile and signals readiness
   over a Unix socket.
4. **Detached Supervisor** calls the **CH Driver** (`internal/core/driver/cloudhypervisor`)
   to configure and launch the `cloud-hypervisor` process (disk, virtiofs mounts, vsock, TAP device).
5. **Supervisor** obtains the TAP fd from the driver's `NetworkHook` and passes it to
   `perimeter.Start` (`internal/core/perimeter/supervisor.go`), which launches the netfilter
   AllowList refresh goroutine and the MITM HTTPS proxy listener.
6. **Guest Agent** (`cmd/nexus-agent`) starts inside the VM, registers on vsock port 1024,
   and waits for gRPC calls.
7. **CLI** dials the agent over vsock (`internal/clientagent`), confirms readiness via `AgentInfo`,
   and returns the sandbox handle to the caller.
