# nexus3 CLI Surface Inventory

Captured: 2026-09-07 from `internal/cli/` on branch `develop`.
Purpose: authoritative inventory of nexus3's own CLI surface. The test `internal/cli/surface_inventory_test.go::TestVerbInventory` fails if the registry diverges from the golden list. `TestSpecCoversAllVisibleVerbs` fails if a visible verb is missing from this file. Both run under `make test`.

---

## Scope

This file documents the full public CLI surface of nexus3 on this branch, including `fork`, `snapshot`, and `restore` (shipped as primitives).

> **Historical note.** An earlier motive charter drafted this file as a parity target for a microsandbox-pivot repo and marked `fork`, `snapshot`, `restore`, `--nested`, and `supervisor-backfill-netns-identity` out-of-scope for that pivot. That pivot was superseded on 2026-08-15 by the strict-primitives turn; nexus3 is now the shipping repo and all verbs below are in-scope.

Rootless/zero-networking-privilege egress mode (old P1 design) and hosted-service/server mode are not exposed as top-level verbs; they influence `egress` internals only.

---

## Hidden verbs (not public CLI surface)

These are registered with `Hidden: true` and do not appear in `nexus3 --help`. They are plugin-private.

| Verb | Summary |
|---|---|
| `__herdr-plugin` | Deprecated alias for `herdr` (kept for installed plugin backward compat) |
| `herdr` | herdr plugin operations (attach, create, list, agent, …) |

---

## Visible verbs

### ## attach

Summary: Reattach to an existing guest session

Flags:
- `--from uint64` — byte offset in the guest output ring to resume from (default 0)

---

### ## auth

Summary: Manage Anthropic authentication

Subcommands: `login`, `logout`, `status`

`auth login` flags:
- `--from string` — source credential file path (default: agent-specific)
- `--force` — allow overwriting an existing complete credential store
- `--agent string` — agent to authenticate; omitting it for claude-code prints a "no longer needed" notice (claude-code sandboxes use a live virtiofs mount of `~/.claude`); partial — only claude-code is registered today

---

### ## config validate

Summary: Load and validate .nexus/config.yaml; print the resolved path and an effective-config summary

No flags beyond global `--json`. Args: `[dir]` (default `.`); the file is located by `config.Load`, searching from `dir` up to the repository root.

On success prints `ok: <path>` plus version, image, containerfile (path or `(absent)`) and an egress summary (`mode=<default|allow-only|policy-gated> allow=<n> policy=<n> secrets=<n>`); `--json` envelope kind is `config_validate`. A missing file or a `config.Load` error is reported as an error and exits 1.

---

### ## config-ssh

Summary: Write an SSH config stanza for a sandbox (ProxyCommand via nexus3 ssh --stdio)

No flags. Args: `<sandbox-ref>`

---

### ## cp

Summary: Copy files between host and guest (guest:\<path\> prefix marks the guest side)

Flags:
- `--dir` — treat the guest path as a directory (archive transfer)

Args: `<src> <dst>`

---

### ## create

Summary: Create a sandbox (flat spelling of `sandbox create`)

Delegates to `sandbox create`. Same flags:
- `--image string` — OCI image reference
- `--rootfs string` — path to an unpacked rootfs directory
- `--file string` — path to a .nexus/Containerfile workspace (build-first create)
- `--dockerfile string`, `-f string` — explicit Containerfile path override
- `--memory uint32` — guest RAM in MiB
- `--vcpus uint32` — number of virtual CPUs
- `--label KEY=VALUE` — attach a metadata label (repeatable)
- `--workspace string` — workspace root for --file builds
- `--rm` — destroy the sandbox on exit
- `--force` — skip the disk-space preflight
- `--nested` — nested KVM (OUT-OF-SCOPE for new repo)
- `--capture-max string` — max bytes for captured output

---

### ## disk

Summary: Report host disk usage by category (usage)

Subverbs:
- `usage` — what nexus3 owns under the state directory by category (allocated bytes), how much is unreferenced, free space vs the builder floor, and next actions (`image prune`, `reap`). No flags beyond global `--json`.

---

### ## doctor

Summary: Report substrate availability and capability check results

No flags.

---

### ## egress

Summary: Egress perimeter management (subcommands: allow, log)

Subcommands:
- `egress log <sandbox> [--follow]` — tail the egress log for a sandbox
- `egress allow <sandbox> <host>` — add an allowlist entry at runtime

---

### ## exec

Summary: Run a command in a sandbox via the in-guest agent

Flags:
- `--pty` — allocate a PTY for the session
- `--rows uint` — terminal rows (requires --pty; default 24)
- `--cols uint` — terminal columns (requires --pty; default 80)
- `--cwd string` — working directory for the command inside the guest

Args: `<sandbox-ref> <cmd> [args...]`

---

### ## fork

Summary: Fork a sandbox into N running children (--count N, default 1)

**OUT-OF-SCOPE for new repo** — fork-from-running snapshots declined.

Flags (documented for reference only):
- `--count int` — number of children (default 1)
- `--force` — force fork even if preflight fails

Args: `<sandbox-ref>`

---

### ## forward

Summary: Port-forward a TCP port from a sandbox to localhost

No flags. Args: `<sandbox-ref> <hostPort>:<guestPort>`

---

### ## harvest

Summary: (herdr worktree harvest — copies build artifacts from sandbox to host)

No flags documented; see cmd_harvest.go.

---

### ## image

Summary: Image management

Subcommands: `build`, `list`, `rm`

`image build` flags:
- `--workspace string` — path to workspace root containing .nexus/Containerfile (default: cwd)
- `--ref string` — human-readable tag, e.g. nexus3-base:20260807 (optional)
- `--base string` — OCI base image reference (default: debian:bookworm-slim)

---

### ## kernel install

Summary: Download and install the guest kernel image from a release

Flags:
- `--version string` — release version to download (default: CLI's own version; required for `-dev` builds unless `NEXUS3_RELEASE_BASE_URL` is set)

Downloads `vmlinux-x86_64` and its sha256 from `${NEXUS3_RELEASE_BASE_URL:-https://github.com/IniZio/nexus3/releases/download}/v<version>/`. Verifies the sha256 before install. Installs atomically (temp + rename) to `$XDG_DATA_HOME/nexus3/images/kernel/vmlinux-x86_64` (default: `~/.local/share/nexus3/images/kernel/vmlinux-x86_64`). Idempotent: exits 0 immediately when the installed file's checksum matches.

---

### ## log

Summary: Stream or print the console log of a sandbox

Flags:
- `-n int` — print only the last N lines
- `--tail int` — alias of `-n`
- `-f` — stream appended lines until interrupted
- `--follow` — alias of `-f`

Args: `<sandbox-ref>`

---

### ## ls

Summary: List sandboxes (alias of `ps`)

Delegates to `sandbox list`. No flags beyond what `ps` accepts.

---

### ## mcp

Summary: Run an MCP server over stdio (JSON-RPC on stdin/stdout, diagnostics to stderr)

No flags.

---

### ## orca

Summary: Orca workspace lifecycle management

Subcommands: `create`, `suspend`, `resume`, `destroy`

---

### ## pause

Summary: Pause a running sandbox (flat spelling of `sandbox pause`)

Delegates to `sandbox pause`. Args: `<sandbox-ref>`

---

### ## ps

Summary: List sandboxes (flat spelling of `sandbox list`)

Delegates to `sandbox list`. No flags.

---

### ## reap

Summary: Reap orphaned supervisor processes and dangling lock files

Flags:
- `--apply` — delete orphaned resources (default: dry-run)

---

### ## recover

Summary: Recover a crashed or wedged sandbox

No flags documented; see cmd_recover.go. Args: `<sandbox-ref>`

---

### ## restore

Summary: Restore N running sandboxes from a snapshot

**OUT-OF-SCOPE for new repo** — restore-from-snapshot declined.

Flags (documented for reference only):
- `--count int` — number of children to restore (default 1)
- `--force` — force restore even if preflight fails

Args: `<snapshot-id>`

---

### ## resume

Summary: Resume a paused sandbox (flat spelling of `sandbox resume`)

Delegates to `sandbox resume`. Args: `<sandbox-ref>`

---

### ## rm

Summary: Remove a sandbox (flat spelling of `sandbox rm`)

Delegates to `sandbox rm`. Args: `<sandbox-ref>`

---

### ## run

Summary: Create, boot, and exec into a sandbox in one step

Flags:
- `--memory uint` — guest RAM in MiB (0 = driver default)
- `--vcpus uint` — number of virtual CPUs (0 = driver default)
- `--name string` — sandbox name (default: generated)
- `--project string` — sandbox project (default: ephemeral)
- `--force` — skip the disk-space preflight

Args: `<image-ref>`

---

### ## sandbox

Summary: Manage sandboxes (create|list|rm|start|stop|pause|resume)

Group verb; subcommands are the lifecycle operations. `sandbox create` flags match `create` above.

---

### ## sandbox agent-upgrade

Summary: Upgrade the nexus3-agent binary running inside a live sandbox

Flags:
- `--agent string` — path to replacement nexus3-agent binary (default: auto-locate via PATH)
- `--force` — force upgrade even if active exec sessions exist (those sessions will be killed)
- `--timeout duration` — maximum time to wait for the new agent to become ready (default 30s)

Args: `<sandbox-ref>`

---

### ## shell

Summary: Open an interactive shell in a sandbox (PTY, raw mode, SIGWINCH forwarded)

No flags. Args: `<sandbox-ref> [-- <cmd> [args...]]`

---

### ## snapshot

Summary: Snapshot management

**OUT-OF-SCOPE for new repo** — snapshot management declined.

Subcommands (documented for reference only): `create <ref>`, `list`, `rm <snapshot-id>`

---

### ## ssh

Summary: Dial a sandbox's sshd over vsock (use as SSH ProxyCommand with --stdio)

Flags:
- `--stdio` — ProxyCommand mode: splice stdin/stdout to the guest's vsock port 22

Args: `<sandbox-ref>`

---

### ## start

Summary: Start a stopped sandbox (flat spelling of `sandbox start`)

Delegates to `sandbox start`. Args: `<sandbox-ref>`

---

### ## stop

Summary: Stop a running sandbox (flat spelling of `sandbox stop`)

Delegates to `sandbox stop`. Args: `<sandbox-ref>`

---

### ## supervisor-backfill-netns-identity

Summary: Backfill the netns identity for a running sandbox supervisor

**OUT-OF-SCOPE for new repo** — CH-specific netns identity backfill not applicable to microsandbox.

No flags. Args: `<sandbox-ref>`

---

### ## supervisor-upgrade

Summary: Hot-swap the detached per-sandbox supervisor to the current binary

Flags:
- `--force` — upgrade even when the running supervisor already reports the current binary

Args: `<sandbox-ref>`

---

### ## version

Summary: Print the nexus3 version

No flags.

---

### ## volume

Summary: Named persistent volume management

Subcommands and their flags:

`volume create`:
- `--kind string` — volume kind: `dir` or `disk` (default: disk)
- `--size int64` — size in bytes for kind=disk (default: 10 GiB)
- `--path string` — host directory path for kind=dir (default: managed)

`volume ls`:
- `--sandbox string` — filter volumes attached to this sandbox ID

`volume rm`: no flags. Args: `<volume-id>`

`volume prune`:
- `--apply` — perform deletions (default: dry-run)
- `--include-detached` — also delete detached volumes (requires --apply)

---

## Integration test inventory

50 integration-tagged test files in `internal/` and `cmd/`. `make test` NEVER compiles these (`//go:build integration`). Each entry has a **disposition** for the new repo:

- **PORT** — test the same behavior against microsandbox primitives; rewrite as needed
- **DROP** — behavior declined by motive; test has no place in new repo
- **REPLACE** — behavior needed but test is tightly coupled to CH internals; rewrite from scratch

| File | Key tests | Disposition |
|---|---|---|
| `internal/core/builder/builderimage/toolchain_integration_test.go` | TestBuilderVMToolchain | PORT |
| `internal/core/builder/builder_integration_test.go` | TestImageBootsAndAgentReachable, TestBuildkitBaseBuild | PORT |
| `internal/core/builder/vmbuilder_recipe_test.go` | TestGuestBuild_RecipeReachesArgv, TestGuestBuild_ZeroRecipeNoRecipeArgs, TestGuestBuild_RecipeRoundTrip, TestGoArchToVendorArch | PORT |
| `internal/core/driver/cloudhypervisor/agent_integration_test.go` | TestAgentExec, TestAgentPTY, TestAgentSnapshotReattach | REPLACE — CH-specific driver; port agent-exec and agent-PTY behaviors, drop snapshot-reattach |
| `internal/core/driver/cloudhypervisor/boot_integration_test.go` | TestBootLifecycle, TestBootToUserspace, TestBrokenBoot_StderrCaptured | REPLACE — CH-specific driver boot path |
| `internal/core/driver/cloudhypervisor/ch_disk_lock_probe_integration_test.go` | TestCHDiskLockProbe | DROP — probes CH-specific concurrent-builder disk lock; not applicable to microsandbox |
| `internal/core/driver/cloudhypervisor/ch_net_integration_test.go` | TestSandboxNet_NoLeakV4V6 | PORT — verify no v4/v6 leak through new substrate |
| `internal/core/driver/cloudhypervisor/ch_netns_lifecycle_test.go` | TestLifecycle_NormalStop_NoLeaks, TestLifecycle_Crash_MemoryLost, TestLifecycle_StopBounded, TestLifecycle_ExplicitKillNoPdeathsig, TestStartCtxCancelDoesNotKillChild, TestLifecycle_ConsoleLogCreated, TestLifecycle_LauncherExitsOnCHDeath | REPLACE — tests CH netns lifecycle internals |
| `internal/core/driver/cloudhypervisor/ch_netns_runtime_integration_test.go` | TestNetnsRuntime_KVMProof, TestNetnsRuntime_CHOrphanKill | REPLACE — CH netns runtime |
| `internal/core/driver/cloudhypervisor/ch_netns_test.go` | TestNetnsSocketpairFiles, TestNetnsChildAttr, TestNetnsSocketpairCloseOrdering | REPLACE — CH netns socket internals |
| `internal/core/driver/cloudhypervisor/ch_vsock_integration_test.go` | TestDialGuest_Integration | REPLACE — vsock dial; if microsandbox exposes vsock, PORT |
| `internal/core/driver/cloudhypervisor/disk_integration_test.go` | TestDiskBoot | REPLACE — CH disk boot internals |
| `internal/core/driver/cloudhypervisor/egress_smoke_test.go` | TestBootEgressSmoke | PORT — egress smoke should run on new substrate |
| `internal/core/driver/cloudhypervisor/fork_isolation_integration_test.go` | TestForkDiskIsolation | DROP — fork declined |
| `internal/core/driver/cloudhypervisor/multidisk_integration_test.go` | TestMultiDisk | PORT — multi-volume attachment |
| `internal/core/driver/cloudhypervisor/snapshot_integration_test.go` | TestSnapshotFork | DROP — snapshot/fork declined |
| `internal/core/driver/cloudhypervisor/virtiofs_e2e_integration_test.go` | TestLiveVirtiofsE2E | PORT — live virtiofs mounts |
| `internal/core/perimeter/cred/refresher_live_test.go` | TestRefresherLiveRefreshGrant | PORT — credential refresh |
| `internal/core/perimeter/egress_e2e_integration_test.go` | TestEgress_GuestOnWire_E2E | PORT — egress E2E |
| `internal/core/perimeter/netstack/netstack_integration_test.go` | TestSandboxNet_AllowAndDenyThroughStack | PORT — netstack allow/deny |
| `internal/test/acceptance/workspace_e2e_test.go` | TestWorkspaceE2E | PORT |
| `internal/test/perimeter/perimeter_e2e_test.go` | TestNetworkHookTracer | PORT |
| `internal/test/selfhost/agent_dogfood_test.go` | TestAgentDogfood | PORT |
| `internal/test/selfhost/autoresize_disk_vcpu_test.go` | TestAutoResizeDiskTelemetry, TestAutoResizeDiskGrowDevice, TestAutoResizeVCPU | PORT |
| `internal/test/selfhost/autoresize_mem_test.go` | TestAutoResizeMemGrow | PORT |
| `internal/test/selfhost/autoresize_stack_test.go` | TestAutoResizeZRAMBeforeWorkload, TestAutoResizeTmpGrowsWithMemTotal | PORT |
| `internal/test/selfhost/baseimage_agent_test.go` | TestBuildAgentBaseImage | PORT |
| `internal/test/selfhost/baseimage_test.go` | TestBuildSelfHostBaseImage | PORT |
| `internal/test/selfhost/build_dogfood_test.go` | TestBuildDogfood | PORT |
| `internal/test/selfhost/builder_vm_e2e_test.go` | TestBuilderVME2E | PORT |
| `internal/test/selfhost/disk_grow_http_evidence_test.go` | TestDiskGrowHTTPEvidence | PORT |
| `internal/test/selfhost/docker_host_image_test.go` | TestExampleNexus3InDocker_BootsMicroVM | PORT — nexus3-in-docker proven; adapt to new repo name |
| `internal/test/selfhost/exec_pump_stress_test.go` | TestExecPumpStressRepro | PORT |
| `internal/test/selfhost/herdr_hello_test.go` | TestHerdrHello | PORT |
| `internal/test/selfhost/motive_dogfood_test.go` | TestMotiveDogfood | PORT |
| `internal/test/selfhost/nested_boot_test.go` | TestNestedBootInner, TestNestedBootNegativeControl | DROP — nested KVM declined |
| `internal/test/selfhost/nested_dogfood_test.go` | TestNestedDogfood | DROP — nested KVM declined |
| `internal/test/selfhost/nested_source_build_test.go` | TestNestedSourceBuild | DROP — nested KVM declined |
| `internal/test/selfhost/oauth_rotation_dogfood_test.go` | TestOAuthRotationDogfood | PORT |
| `internal/test/selfhost/orca_cred_broker_test.go` | TestOrcaCredBrokerWiring | PORT |
| `internal/test/selfhost/orca_ssh_test.go` | TestOrcaSSH | PORT |
| `internal/test/selfhost/orca_supervisor_test.go` | TestOrcaSupervisorWiring | PORT |
| `internal/test/selfhost/selfhost_e2e_test.go` | TestSelfHostE2E | PORT |
| `internal/test/selfhost/supervisor_s3_test.go` | TestSupervisorS3RefresherWiring, TestSupervisorS3CAInGuest | PORT |
| `internal/test/selfhost/supervisor_s4_test.go` | TestSupervisorS4BoundedRetryReady, TestSupervisorS4OrphanReconcile, TestSupervisorS4PlaceholderInGuest, TestSupervisorS4LiveEgress | PORT |
| `internal/test/selfhost/supervisor_smoke_test.go` | TestSupervisorPostExitEgress | PORT |
| `internal/test/selfhost/tracer_launch_test.go` | TestTracerLaunch | PORT |
| `internal/test/selfhost/worktree_source_fidelity_test.go` | TestWorkspaceSourceFidelity | PORT |

Summary: 47 files total — **PORT 38**, **REPLACE 7**, **DROP 7** (fork/snapshot/restore: 2; nested KVM: 3; CH-specific lock probe: 1; snapshot reattach in agent test counted in REPLACE above).
