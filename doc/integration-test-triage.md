# Triage: the `//go:build integration` suite

**Status as found (2026-09-01):** 44 files carried `//go:build integration`.
`grep -rn "tags integration" Makefile .github/` returned **nothing**. None of
them had ever run. "The suite is green" had never included any of them.

This document is the per-file triage, the tier assignment, and the evidence for
each. It is the reference for `make test-integration` and the
`integration-tier1` CI job.

## Correction to the "45 files" figure

`grep -rl "go:build integration"` reports **45**, but one of those is a false
positive: `internal/core/driver/cloudhypervisor/ch_netns_test.go` matches only
inside a *comment* that says

> `// Runtime skip guards (no //go:build integration tag needed — the test binary ...`

It carries no build tag at all and already runs in ordinary `make test`. The
accurate count of tag-guarded files is **44**. Use the anchored pattern:

    grep -rl '^//go:build integration' --include=*.go .   # → 44

## Tiers

| Tier | Meaning | Files |
|---|---|---|
| **1** | Runnable in ordinary CI — no KVM, no VM, no docker, no image | **1** |
| **2** | Needs `/dev/kvm` + cloud-hypervisor, and usually docker | **41** |
| **3** | Does not compile under the tag | **2** |

**The headline finding is that number: exactly one of 44 files is runnable in
ordinary CI.** The tag is overwhelmingly a hardware-integration suite, and
treating it as "tests we forgot to switch on" would be wrong — 41 of the 44 can
only ever run on a machine with nested virt, a cloud-hypervisor binary, boot
artifacts, and a docker daemon.

## Environment used for the evidence below

This slice ran inside a nexus3 guest VM:

| Prerequisite | State |
|---|---|
| `/dev/kvm` | present (nested virt on) |
| `cloud-hypervisor` | `/usr/local/bin/cloud-hypervisor` |
| `mke2fs`, `debugfs`, `virtiofsd` | present |
| **`docker`** | **ABSENT** — this is what skips most of Tier 2 |
| boot artifacts | fetched via `scripts/fetch-boot-artifacts.sh` (SHA-verified) |
| `cpio` | absent initially; installed via apt to build the initramfs |
| MemAvailable | ~1.3 GB |
| euid | **root** (`CapEff=0x1ffffffffff`) — see the CAP_NET_ADMIN note below |

`systemd-run` does not exist in this guest, so `CAPPED` fails closed as designed;
all runs used the documented `NEXUS3_ALLOW_UNCAPPED=1` escape, exactly as CI does.

---

## Tier 1 — runnable in ordinary CI (1 file)

| File | Asserts | Result |
|---|---|---|
| `internal/core/perimeter/netstack/netstack_integration_test.go` | `TestSandboxNet_AllowAndDenyThroughStack` — injects crafted Ethernet/IPv4/TCP SYN frames into a socketpair and drives a real allow and a real deny through Stack → gvisor VirtualNetwork → AllowList → AuditEvent | **PASS** |

Pure userspace (gvproxy's VirtualNetwork needs no CAP_NET_ADMIN and no TUN
device), so it runs anywhere a Go toolchain runs. This is what
`make test-integration` runs by default and what the `integration-tier1` CI job
runs.

### Caveat: this package has an in-guest TestMain guard

`internal/core/perimeter/netstack/netstack_test.go:35` exits 0 when
`/proc/1/comm == "nexus3-agent"`. Inside a nexus3 guest the package prints

    netstack: skipping tests — running inside nexus3 guest VM (host-side package)

and reports `ok` having run **nothing**. In-guest greens for this package are
meaningless. Verify with `unshare --pid --mount-proc --fork`, which changes
`/proc/1/comm` and produces a real run. On a CI runner the guard does not
trigger, so the CI job is a genuine execution.

Five packages carry this guard, not two:

    internal/cli/testmain_test.go
    internal/core/perimeter/netstack/netstack_test.go
    internal/core/recovery/testmain_test.go
    internal/core/service/testmain_test.go
    internal/mcp/testmain_test.go

---

## Tier 3 — does not compile (2 files) → recommend repair, not deletion

Both are genuine bit-rot: the production API moved and, because the tag was
never built, nothing noticed.

| File | Compile error | Cause |
|---|---|---|
| `internal/test/acceptance/workspace_e2e_test.go:470` | `cannot convert func(resolvedExt4 string) (driver.Driver, error) to type service.DriverFactory` | `service.DriverFactory`'s signature changed |
| `internal/test/perimeter/perimeter_e2e_test.go:186` | `unknown field EnableNetHook in struct literal of type cloudhypervisor.Config` | `Config.EnableNetHook` was removed |

**I am not deleting either.** Both encode real acceptance behaviour
(`TestWorkspaceE2E`, `TestNetworkHookTracer`) and both failures are one-line
signature drift, not obsolescence. Deleting them would destroy coverage to make
a number look better. They stay red and out of CI until repaired; `make
vet-integration` is the target that surfaces them.

Note `go vet` reports only the **first** error per package, so each of these may
hide further errors behind it. The two above are what a full type-check
(`go test -tags integration -run ZZZNONE`) reports.

---

## Tier 2 — needs /dev/kvm + cloud-hypervisor (41 files)

### 2a. Verified PASSING against real booted VMs on this host (9 files)

These genuinely boot cloud-hypervisor microVMs under nested virt and pass.

| File | Tests | Result |
|---|---|---|
| `cloudhypervisor/boot_integration_test.go` | `TestBootLifecycle`, `TestBootToUserspace`, `TestBrokenBoot_StderrCaptured` | PASS |
| `cloudhypervisor/ch_vsock_integration_test.go` | `TestDialGuest_Integration` | PASS **after a fix** (below) |
| `cloudhypervisor/ch_disk_lock_probe_integration_test.go` | `TestCHDiskLockProbe` | PASS |
| `cloudhypervisor/ch_net_integration_test.go` | `TestSandboxNet_NoLeakV4V6` | PASS (needs `/dev/net/tun` + CAP_NET_ADMIN, so not ordinary CI) |
| `cloudhypervisor/disk_integration_test.go` | `TestDiskBoot` | PASS |
| `cloudhypervisor/egress_smoke_test.go` | `TestBootEgressSmoke` | PASS |
| `cloudhypervisor/fork_isolation_integration_test.go` | `TestForkDiskIsolation` | PASS |
| `cloudhypervisor/multidisk_integration_test.go` | `TestMultiDisk` (4 subtests) | PASS |
| `cloudhypervisor/snapshot_integration_test.go` | `TestSnapshotFork` | PASS |

#### Fix applied: `ch_vsock_integration_test.go`

Failed with:

    DialGuest: unexpected error: ... read handshake reply: EOF
    (guest agent not yet listening on vsock port 1024 — VM may still be starting up)

The test's own doc comment declares two acceptable outcomes: a handshake NACK,
or "a connection error if CH hasn't started the multiplexer socket yet". Its
substring allowlist was `no such file` / `connection refused` / `NACK`.
`ch_vsock.go:137-145` later added a distinct EOF branch whose own comment reads:

> the vsock multiplexer is up (the AF_UNIX connect succeeded) but nothing is
> listening on port %d inside the VM yet. This is the signature of a race
> between the host dialer and in-guest agent startup, **not a transport fault**.

So the product was correct and the assertion was stale. Added
`guest agent not yet listening` to the allowlist. This aligns the assertion with
the contract the test already documented; it does not weaken it.

### 2b. FAILING on this host — cause identified, NOT papered over (6 files)

#### (i) Root-context precondition — 2 files, functionally green

| File | Test | Detail |
|---|---|---|
| `internal/core/perimeter/egress_e2e_integration_test.go` | `TestEgress_GuestOnWire_E2E` | every functional assertion PASSED: ALLOW probe + token swap, DENY AuditEvent, IPv6 zero-egress, zero-egress after `supervisor.Close()`. Only the pre/post check `host has CAP_NET_ADMIN set (CapEff=0x1ffffffffff, bit 12 SET)` failed. |
| `cloudhypervisor/ch_netns_test.go` (untagged, same class) | `TestNetnsRuntime_KVMProof` | functional assertion PASSED (`1 frame(s) with guest MAC ... received on parent PerimConn`); only the CAP_NET_ADMIN check failed. |

These assert that the **host** process does *not* hold CAP_NET_ADMIN — the whole
point of the netns-runtime design. I ran as root, so `CapEff` has every bit set
and the assertion correctly fails. **This is an environment mismatch, not a
product bug, and not test rot.** I deliberately did not relax it: weakening a
security assertion to get a green is the opposite of the job. These require an
unprivileged host user.

`ch_netns_test.go` is untagged, so it runs in ordinary `make test`. It skips when
boot artifacts are absent — but `testdata/` is gitignored, and once
`scripts/fetch-boot-artifacts.sh` has been run on a machine where tests execute
as root, `make test` itself goes red here. Worth knowing before someone fetches
artifacts and is surprised.

#### (ii) The 2-second agent-readiness budget — 4 files

| File | Tests | Symptom |
|---|---|---|
| `cloudhypervisor/agent_integration_test.go` | `TestAgentExec`, `TestAgentPTY`, `TestAgentSnapshotReattach` | `read handshake reply: EOF (guest agent not yet listening on vsock port 1024)` |
| `cloudhypervisor/virtiofs_e2e_integration_test.go` | `TestLiveVirtiofsE2E` — 5 of 7 subtests (AC1, AC2a, AC2b, AC3, AC6). AC4 and AC5 PASS | same |
| `internal/core/builder/builder_integration_test.go` | `TestImageBootsAndAgentReachable` | `vsock read reply: EOF`. (`TestBuildkitBaseBuild` skips: no buildkitd) |
| `internal/test/selfhost/builder_vm_e2e_test.go` | `TestBuilderVME2E` | same class |

All four share one root cause. `agent_integration_test.go:272-281` waits for the
guest agent with a **fixed 2-second sleep**, and its comment is explicit:

> This fixed budget is also the regression guard for the boot-critical-path
> defect: any blocking work added to PID-1 startup AHEAD of the vsock listeners
> ... pushes the bind past this sleep and every agent test fails with "read
> handshake reply: EOF". **Do not raise it to make a slow boot pass** — move the
> slow work off the pre-bind path instead.

Captured serial output shows the guest kernel had only reached ~1.85 s
(`NET: Registered PF_VSOCK protocol family`) and had **not yet entered
userspace** when the test gave up. Under nested virtualisation the kernel alone
needs longer than the 2 s budget to reach `/init`.

**Honest verdict: inconclusive on this host, and I did not change these tests.**
The budget is calibrated for bare metal. I cannot show a product regression from
this evidence, and I will not raise a sleep the code explicitly forbids raising
to manufacture a green. These need a bare-metal (non-nested) KVM host to give a
trustworthy signal — which is exactly why they are not in CI.

### 2c. SKIPPED here — docker absent (26 files)

Every one of these skips cleanly with an explanatory message, because they bake a
rootfs with docker + `mke2fs` before booting. `docker` is not installed in this
guest, so **none of them were actually exercised**.

`internal/test/selfhost/`: `agent_dogfood`, `autoresize_disk_vcpu`,
`autoresize_mem`, `autoresize_stack`, `baseimage`, `baseimage_agent`,
`build_dogfood`, `disk_grow_http_evidence`, `docker_host_image`,
`exec_pump_stress`, `herdr_hello`, `motive_dogfood`, `nested_boot`,
`nested_dogfood`, `nested_source_build`, `oauth_rotation_dogfood`,
`orca_cred_broker`, `orca_ssh`, `orca_supervisor`, `selfhost_e2e`,
`supervisor_s3`, `supervisor_s4`, `supervisor_smoke`, `tracer_launch`,
`worktree_source_fidelity` (25 files)

plus `internal/core/builder/builderimage/toolchain_integration_test.go` and
`internal/core/perimeter/cred/refresher_live_test.go` (the latter needs a real
OAuth cred store, not docker).

**A run of this group reports `ok`. That green means nothing.** It is the same
false-confidence shape as a cached green or an unreferenced build tag, which is
precisely why `make test-integration` does not default to `./...`.

---

## Running it

    make test-integration              # Tier 1 only (default) — safe anywhere
    make vet-integration               # type-check ALL 44 (currently 2 RED)

    # Tier 2, explicitly, on a host with /dev/kvm + cloud-hypervisor:
    bash scripts/fetch-boot-artifacts.sh
    make test-integration GOTEST_PKGS=./internal/core/driver/cloudhypervisor/ \
      GOTEST_P=1 GOTEST_PARALLEL=1 GOTEST_ARGS='-timeout 25m'

`test-integration` carries `-count=1` for the same reason `test` does. Proven,
not assumed — with identical flags, the second run without `-count=1` reports
`(cached)`:

    go test -race -tags integration ./...netstack/   → ok  1.142s
    go test -race -tags integration ./...netstack/   → ok  (cached)
    go test -race -tags integration -count=1 ./...netstack/ → ok  1.147s

## Recommended next steps

1. Repair the 2 Tier 3 files (one-line signature drift each) and add
   `vet-integration` to CI. A tag that does not compile will rot again.
2. Run Tier 2 on a bare-metal KVM host with docker and an unprivileged user, on
   a schedule rather than per-PR. That is the only environment in which the
   remaining 41 files produce a trustworthy signal.
3. Nothing here should be deleted. No file was found obsolete.
