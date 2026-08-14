# 14 — Compose-monorepo herdr/orca proof: evidence-based gap map

*Evidence gathered: 2026-08-14. All citations are against the uncommitted working tree on `milestone-a-agent-sandbox`.*

---

## Top-line verdict

**NO-GO today.** The core path is proven — the 2026-08-13 compose pilot ran `db redis api` inside a
nexus3 sandbox using the same `nexus3 sandbox create --file` call that the herdr TUI wraps. The
herdr integration adds no new code risk; it just shells out to the same CLI commands.

**The proof cannot proceed today because Probe B (host RAM) failed.** Probe A (capture size) has
been completed and **passes** — see GAP-1. Probe B was run on 2026-08-14 and **failed**: the
host has only **12.0 GiB** of free RAM (MemAvailable: 12,580,592 kB) against the ≥ 20 GB pass
condition, and swap is **99.94% exhausted** (~5 MB of 8.0 GiB free), leaving no elastic buffer.
The verdict flips to GO when RAM is reclaimed to ≥ 20 GB free and swap is restored to give
headroom — stop idle sandboxes, reboot if necessary, and re-run Probe B before starting the
full-stack run.

**Most likely first failure — CONFIRMED**: Host RAM headroom for the full 20-service stack
(GAP-3). Probe B was run on 2026-08-14 and **confirmed** this prediction: the host has only
12.0 GiB free RAM with swap 99.94% exhausted — well below the ≥ 20 GB pass condition. The
auto-resize governor grows memory under pressure but has a 60-second cooldown per cycle; on a
host already at 12.0 GiB with no swap headroom, governor growth cannot save the run. Stop all
idle sandboxes and reclaim RAM to ≥ 20 GB before attempting the proof.

**Second most likely failure**: dockerd is not started automatically. The image ships a
`nexus-dockerd-up` helper script but nothing calls it; the operator must run it explicitly via
`nexus3 exec` (or `nexus3 sandbox exec`) before `docker compose up`. Forgetting this step wastes
a compose invocation that hangs with "Cannot connect to the Docker daemon".

---

## What the prior pilot proved

The 2026-08-13 compose pilot (`session 54a47dd5`) constitutes prior art for the core path:

```
nexus3 sandbox create compose/pilot --file <target-monorepo> --memory 8192
nexus3 exec compose/pilot sh -lc 'nexus-dockerd-up && docker compose up -d db redis api'
```

Proven independently and advisor-APPROVED:
- In-guest docker 29.1.3 / overlay2 started successfully
- `db` (postgres) + `redis` healthy
- `api` serving HTTP 200 at `/docs` with live db:5432 + redis:6379 connections

**Not proven in that pilot:**
- Full 20-service stack (memory limit: 1 GB held only 4 services with ~521 MiB free; pilot ran
  at 8 GiB which is adequate for the subset)
- Herdr TUI as the launch surface (the pilot used the CLI directly)
- Port publishing to the host for browser access
- `network_mode: service:*` services (api/admin join authgear-* netns; compose requires dockerd
  which requires K1 kernel; all of this is mutually dependent)
- Working-tree capture via `WorktreeToDisk` (pilot used `git archive HEAD`)

The pilot used `git archive HEAD` for the build context. The code was replaced with
`WorktreeToDisk` after the pilot (memory: nexus3-worktree-source-init-done, 2026-08-13).
The functional contract is unchanged; the size-cap risk this introduced has been resolved — see GAP-1.

---

## What "herdr/orca" means here

The Orca GUI-composer path is **abandoned** (remote client's "Run on" picker does not show VM
recipes; host is headless). The intended path is the herdr TUI plugin:

1. herdr calls `__herdr-plugin space-create-from-file` via `plugins/herdr/bin/pane.sh`
2. `space-create-from-file` (`cmd_herdr_plugin.go:376`) calls
   `exec.CommandContext(ctx, exe, "sandbox", "create", handle, "--file", contextDir)` — this is
   the identical call sequence the pilot used directly
3. After boot, herdr's `attach` pane opens a PTY shell into the guest

The herdr plugin manifest is at `plugins/herdr/herdr-plugin.toml`; the shim is at
`plugins/herdr/nexus3-shim.sh`; the pane scripts are in `plugins/herdr/bin/`. The herdr binary
version floor is `0.7.4` (the min_herdr_version field). At v0.8.0 (Apache-2.0), the plugin
should load.

---

## Gaps ordered by what blocks the proof earliest

### GAP-1 — Source tree capture size [wired]

**What it blocked**: The very first step (`nexus3 sandbox create --file`). If `WorktreeToDisk`'s
free-space guard trips, the image build fails immediately. **This gap is now cleared** — see
measured evidence below.

**Measured evidence** (probe run 2026-08-14, production `preflightCaptureSize` in AUTO mode
against the real target monorepo's working tree):

- Effective capture after `.dockerignore` AND nexus3's always-exclude list: **6.36 GiB**
- Largest contributors: `web` 2.06 GiB, `backend` 1.67 GiB, `loadtest` 1.58 GiB, `deploy` 583.54 MiB, `.cocoindex_code` 191.08 MiB
- `.dockerignore` coverage **verified**: it excludes `.git` and `node_modules`; the 6.36 GiB survivors are genuine source tree, not build detritus
- Projected ext4 image: ~12.8 GiB (6.36 × 2 + 64 MiB headroom)
- Host free space at probe time: 31–33 GB available on `/` (80 % threshold ≈ 25–26 GB)
- **Verdict: PASSES** — 12.8 GiB is well within the 25–26 GB threshold

**Margin note**: The 12.78 GiB projection sits against 24.1 GiB of headroom (80 % × 30.1 GiB
currently free on `/`, disk at 94 % used as of 2026-08-14). A single additional large capture or
artifact disk erases that margin; if host disk usage grows before the proof runs, re-run Probe A.

**Evidence dependency**: The projected image size is computed as
`capturedSize × imageSizeHeadroomFactor + 64 MiB`, where `imageSizeHeadroomFactor` is the ×2
constant defined in `internal/core/builder/ext4.go`. The pass verdict rests on this constant holding at ×2. That constant is now anchored: a test in
`worktreedisk_test.go` pins the ×2 factor specifically — changing `imageSizeHeadroomFactor` from
2 to 1 turns it red, as does dropping the ×2 from the projection while keeping the
`+imageMinSizeBytes` term. The projection formula as a whole is not exhaustively verified, but
the ×2 factor that this evidence depends on is.

**HISTORICAL — pre-probe estimate (superseded)**: The target monorepo's working directory was reported
at 9.6 GB unfiltered (`du -sh --exclude=.git`), with the remaining ~9.5 GB above the known
58 MB `node_modules` attributed to suspected build-artifact directories
(`authgear-teacher-student-target/`, `authgear-admin-target/`). Whether `.dockerignore` excluded
those directories was unverified, and a 2 GiB hardcoded cap (`DefaultCaptureMaxBytes`) was in
effect at that time. Both figures are superseded: the constant cap was replaced by the
free-space-derived guard, and the probe confirmed `.dockerignore` already covers the large
directories. Retained so a future reader does not rediscover and re-believe the 9.6 GB / 2 GiB
framing.

**HISTORICAL — probe commands (superseded by measured results above)**:
```bash
cat <target-monorepo>/.dockerignore
du -sh <target-monorepo>/authgear-*-target 2>/dev/null
du -sh <target-monorepo>/.pnpm 2>/dev/null
```
If `.dockerignore` excludes the large dirs and the filtered tree is small enough that the projected
image fits on the host, proceed. If not, add the large dirs to `.dockerignore` before running the
proof (or pass `--capture-max <size>` to override the guard with an explicit cap).

---

### GAP-2 — dockerd not started automatically [partial]

**What it blocks**: Any `docker compose up` invocation. Without this step, compose exits
immediately with "Cannot connect to the Docker daemon."

**Evidence**: `internal/core/agent/` has no `docker_linux.go` (confirmed: NO_DOCKER_FILES). The
D0 agent-dockerd-autostart slice is not built. However, the `.nexus/Containerfile` bakes in a
`/usr/local/bin/nexus-dockerd-up` script that does the equivalent:

```sh
mount --make-shared / 2>/dev/null || true
docker info >/dev/null 2>&1 && exit 0
dockerd --storage-driver=overlay2 --iptables=false >/tmp/nexus-dockerd.log 2>&1 &
# polls up to 15 s then exits 0 or 1
```

**Required manual step** before compose: `nexus3 exec compose/pilot sh -lc nexus-dockerd-up`
(or inside an attached shell: `nexus-dockerd-up`). This must succeed before proceeding.

**Why it is not "missing"**: The workaround is in the committed Containerfile and proven in the
prior pilot. The gap is only that it requires an explicit operator action rather than being
automatic.

---

### GAP-3 — Host RAM for the full 20-service stack [missing]

**What it blocks**: Bringing up all 14 dev-mode services simultaneously. The prior pilot proved
4 services on 8 GiB with ~521 MiB free — RAM was the binding constraint.

**Evidence**: The `nexus3-docker-compose` motive notes "full ~20-service stack NOT yet run —
needs the new `--memory` exercised (1 GB held only 4 services)." Dev-mode services in
`docker-compose.yml`: `student`, `teacher`, `storybook`, `api`, `admin`, `worker`, `scheduler`,
`mailpit`, `minio`, `minio-setup`, `db`, `redis`, `authgear-teacher-student`, `authgear-admin`,
`authgear-teacher-student-role-creation-tasks-enqueuer` — roughly 14–15 services. The auto-resize
governor (doc 13, section 4) will grow memory on PSI ≥ 10 % or available/total ≤ 20 %, with a
60-second cooldown per grow cycle. Host-resource guards are **per-axis and asymmetric**:

- **Memory grows**: `g.headroom.HasHeadroom(ctx, target-current)` at `memory.go:330`
  (inside `if !isShrink`) consults `/proc/meminfo` MemAvailable before committing a grow.
- **Disk grows**: `checkFreeSpace(diskPath, targetBytes)` at `driver_resize.go:208` checks
  host disk free space (sparse-aware) — this is a disk guard, **not** a RAM guard.
- **vCPU grows**: **no host resource guard exists.** On a RAM-starved host the governor
  will add vCPUs unconditionally.

**Measured result (Probe B, 2026-08-14 — FAIL)**:
- MemAvailable: **12.0 GiB** (12,580,592 kB) — pass condition ≥ 20 GB — **FAILS**
- SwapFree: **~5 MB of 8.0 GiB** — swap 99.94% exhausted, no elastic buffer
- One live `cloud-hypervisor` process running at time of measurement
- Host disk: 30.1 GiB available on `/`, 94% used

**Exposure on this host (12.0 GiB RAM available, swap exhausted)**: memory grows will stall
immediately — the governor's MemAvailable guard at `memory.go` will refuse to commit a grow when
the host is this constrained; vCPU grows will proceed regardless, adding scheduling pressure
without helping RAM. The known host-OOM wall from the nested-build runs (root-caused 2026-08-14)
is RAM-specific; it does not protect against vCPU scheduling pressure added to a saturated host.
Do not start the proof until RAM is ≥ 20 GB free.

**Re-measurement 2026-08-14 (after docker services stopped)**:
- MemAvailable: **20.4 GiB / 21.9 GB** (21,370,464 kB) — pass condition ≥ 20 GB — **PASSES**
- SwapFree: **~1.3 GB of 8.0 GiB** — swap headroom restored
- The original FAIL measurement (12.0 GiB, swap 99.94% exhausted) is retained above as the record of why the NO-GO was issued.
- **Operator decision required**: this re-measurement is above the pass condition. Whether to flip the top-line verdict is the operator's call; this document records the evidence but does not change it unilaterally.

**HISTORICAL — original probe commands (superseded by measured results)**:
```bash
free -h   # how much host RAM is free right now?
# is the existing compose/pilot sandbox still running?
nexus3 --json sandbox list | jq '.[] | select(.project=="compose")'
```
If < 20 GB free on the host, stop the pilot sandbox first (but see GAP-7 below re: paused →
stopped). Recommend 32 GB free before starting the full-stack run.

---

### GAP-4 — herdr TUI not proven end-to-end for compose [partial]

**What it blocks**: Confidence that herdr correctly drives the launch. It does NOT block the core
functionality.

**Evidence**: `internal/test/selfhost/herdr_hello_test.go` — `TestHerdrHello` proves the herdr
→ service layer integration (boots a sandbox with `motiveID="herdr"`, runs `/bin/echo`), but
explicitly notes: "The herdr binary is NOT required for this proof." The `space-create-from-file`
subcommand (`cmd_herdr_plugin.go:460`) is `exec.CommandContext(ctx, exe, "sandbox", "create",
handle, "--file", contextDir)` — this is the CLI the pilot used directly. No docker-compose-
specific logic is in the herdr path; herdr just wraps the CLI call.

**Assessment**: If `nexus3 sandbox create --file` works (pilot-proven), the herdr TUI wrapping
it works. The risk is exclusively the operational steps (running the right pane, configuring
`NEXUS3_WORKSPACE`, ensuring the herdr binary is installed at `min_herdr_version = "0.7.4"`).

**Quick check** (2 minutes): `herdr --version` and `nexus3 __herdr-plugin abi` to confirm the
binary and ABI handshake.

---

### GAP-5 — Port publishing for host browser access [partial]

**What it blocks**: The operator browsing to frontends (student, teacher, storybook) from the
host machine. Does NOT block compose-up itself or intra-guest service communication.

**Evidence**: `internal/cli/cmd_forward.go` + `internal/core/service/forward_ops.go` implement
`nexus3 forward <ref> <hostPort>:<guestPort>` — binds `127.0.0.1:<hostPort>`, dials vsock port
3001 (the in-guest agent's port-forward multiplexer), sends a 4-byte big-endian port number, and
splices traffic. This mechanism is fully implemented. For compose services, the container port
is published to the guest TCP stack by docker's iptables rules; then `nexus3 forward` makes it
reachable on the host.

**What is not proven**: `nexus3 forward` carrying traffic through a guest-side docker container
port. The motive explicitly deferred the PF0 verification spike (D-DC-06). The mechanism is
architecturally complete; the unknown is whether docker's iptables NAT inside the guest is
reachable via `nexus3 forward` (it should be — both are on the guest's loopback — but has not
been live-tested with a container port).

**At proof time**: after compose up, run:
```bash
nexus3 forward compose/pilot 3000:3000  # student dev server
```
and try `curl http://localhost:3000`. If that responds, port forwarding works for compose ports.

---

### GAP-6 — Shadow disks for nested package directories [partial]

**What it blocks**: Build context size and workspace disk OOM for workspaces with multiple
per-package `node_modules` trees.

**Evidence**: `internal/cli/shadowdisk.go` — `DefaultShadowDirs = ["node_modules", ".next",
"target", "dist"]`. The comment explicitly states: "For monorepos that contain nested copies
(e.g. `packages/web/node_modules`) callers must supply an explicit override slice." The target
monorepo has `student-node-modules`, `teacher-node-modules`, `storybook-node-modules` named as
Docker volumes in `docker-compose.yml` — these are Docker-managed volumes, not host paths, so
they do not exist on the host working tree and are not a capture problem. The root `node_modules/`
(58 MB via pnpm symlinks) is in DefaultShadowDirs and will be shadowed. No nested `packages/*/
node_modules` paths are present on the host (pnpm places all packages in the root `node_modules`
or the global store). This gap is likely not a real blocker for the target compose monorepo
specifically, but the probe in GAP-1 (checking the actual large directories) resolves it
definitively.

---

### GAP-7 — Lifecycle: paused → stopped is an illegal transition [partial]

**What it blocks**: Operator accidentally `nexus3 sandbox stop` on a paused sandbox (e.g., after
`nexus3 sandbox pause` or `herdr space-pause`). The error is a lifecycle violation, not a crash.

**Evidence**: Doc 13 (section 1, "Findings vs doc 06") — "Corrected 2026-08-14: `paused →
stopped` is NOT a legal transition. `nexus3 stop` on a paused sandbox returns
`IllegalTransitionError`; the operator must `nexus3 sandbox resume` first, then stop."

**At proof time**: If the sandbox is accidentally paused, run
`nexus3 sandbox resume compose/pilot` before any `nexus3 sandbox stop`. If the long-running proof
is interrupted by the herdr pane closing (which calls `herdr space-pause`), the sandbox is in
`paused` state — resume it to continue or remove it cleanly.

---

### GAP-8 — Runtime egress for compose builds [wired]

**Evidence**: `internal/core/service/service.go:651-664` — the `--file` path automatically grants
72-hour AllowAll egress:

```go
// AllowAll sandboxes (--file path, no credentials) skip the curated allowlist;
al.AllowAllFor(72 * time.Hour) // generous window; supervisor restarts reset it
```

This was confirmed by the pilot (commit `e3c2781 perimeter AllowAll`) and is currently in the
service layer for all `sandbox create --file` invocations. `docker compose up` image pulls,
`apt-get`, and `pnpm install` will all succeed. No operator action is needed for egress.

---

### GAP-9 — network_mode: "service:authgear-*" [partial]

**What it blocks**: `api` and `admin` services (which join the authgear container netns).

**Evidence**: `docker-compose.yml` shows:
```yaml
api:
  network_mode: "service:authgear-teacher-student"
admin:
  network_mode: "service:authgear-admin"
```

`service:*` requires the named container to exist and be running first. The kernel `veth` and
`network namespace` support were confirmed present in the original (non-docker) kernel; the
rebuilt K1 kernel (`scripts/kernel/config-6.12.76`, Linux 6.12.76, DONE per memory card
2026-08-12) adds CONFIG_BRIDGE so dockerd starts, but the `service:*` wiring itself is a docker
feature that only requires the netns-join capability (in-kernel since 2.6.x). This is
**partial** only in the sense that it depends on authgear-teacher-student and authgear-admin
being up first (ordering constraint, not a code gap). The prior pilot did NOT test this ordering.

**At proof time**: bring up authgear-teacher-student and authgear-admin first:
```bash
docker compose up -d authgear-teacher-student authgear-admin
# wait for them to be Running
docker compose up -d api admin
```

---

### GAP-10 — Guest kernel with CONFIG_BRIDGE + netfilter [wired]

**Evidence**: `scripts/kernel/build.sh` + `scripts/kernel/config-6.12.76` exist. `images/kernel/
PINNED.md` records: "Built from upstream Linux 6.12.76 source using `scripts/kernel/build.sh`."
Memory card (`nexus3-kernel-bridge-netfilter-done.md`): live-proven + advisor-APPROVE 2026-08-12;
committed, unpushed. The `config-6.12.76` file contains CONFIG_BRIDGE=y,
CONFIG_BRIDGE_NETFILTER, NF_TABLES, IP_NF_NAT, NF_CONNTRACK, IP_NF_IPTABLES — all built-in
(CONFIG_MODULES still unset). Docker's `docker0` bridge and iptables NAT are unblocked.

---

## Summary table

| Gap | Classification | Evidence | What it blocks |
|-----|---------------|----------|----------------|
| GAP-1: Source tree capture size | **wired** | Probe 2026-08-14: 6.36 GiB captured (`.dockerignore` verified), projected 12.8 GiB ext4, host 31–33 GB free — **PASSES** 80 % guard | Cleared — not a blocker |
| GAP-2: dockerd not autostarted | **partial** | No `agent/docker_linux.go`; workaround in `.nexus/Containerfile` | `docker compose up` unless `nexus-dockerd-up` run first |
| GAP-3: Host RAM for full 20-service stack | **missing** | Probe B 2026-08-14 FAIL: MemAvailable 12.0 GiB (pass ≥ 20 GB), swap 99.94% exhausted | Stack cannot start; reclaim RAM to ≥ 20 GB and re-run Probe B |
| GAP-4: herdr TUI E2E not run for compose | **partial** | `TestHerdrHello` proves service layer; herdr wraps same CLI | Low — herdr path calls same `nexus3 sandbox create` |
| GAP-5: Port publish for host browser | **partial** | `cmd_forward.go` + `forward_ops.go` wired; compose port forwarding unproven | Operator browser access to frontends |
| GAP-6: Shadow disks for nested node_modules | **partial** | `shadowdisk.go` top-level only; target monorepo's Docker volumes avoid the problem | Likely not a blocker; verify in GAP-1 probe |
| GAP-7: paused → stopped illegal transition | **partial** | Doc 13 section 1; `IllegalTransitionError` at service layer | Operator cleanup trap; requires resume-first |
| GAP-8: Runtime egress for compose builds | **wired** | `service.go:658` AllowAll for `--file` path (72 h window) | Not blocked — egress is open |
| GAP-9: network_mode: service:* ordering | **partial** | Docker netns-join in kernel since 2.6.x; authgear must start first | api/admin services if wrong bring-up order |
| GAP-10: Guest kernel bridge + netfilter | **wired** | `scripts/kernel/config-6.12.76`; PINNED.md Linux 6.12.76; memory 2026-08-12 | Not blocked — kernel is done |

**Additional wired items** (no gaps, included for completeness):
- Working tree capture: the `WorktreeToDisk` call on the `--file` path in `cmd_sandbox.go`; `WorkspaceSpec` in `service/create.go`
- nexus3 harvest write-back: `service/harvest.go` + `cli/cmd_harvest.go`
- Auto-resize governor (memory/CPU/disk): fully wired per doc 13 section 4; `loop.go`, `memory.go`, `cpu.go`, `disk.go`
- MITM proxy AllowAll: `perimeter/mitm/proxy.go:82`; used by builder VM and by `--file` runtime sandboxes

---

## Classification counts

| Label | Count |
|-------|-------|
| wired | 9 |
| partial | 6 |
| missing | 1 (GAP-3: host RAM measured 12.0 GiB on 2026-08-14; pass condition ≥ 20 GB not met) |
| unknown-needs-probe | 0 |

**Note on "missing"**: This label covers both unbuilt code slices and confirmed absent resources.
GAP-3 is a resource gap: the required host RAM condition (≥ 20 GB free) was measured on 2026-08-14
and was not met. The three originally unbuilt code slices from the motive plan (D0 dockerd-autostart,
C0 compose image template, E0 egress profile) all have working workarounds and are accounted for
under `partial` or `wired` entries: D0 is replaced by `nexus-dockerd-up` in the Containerfile
(proven), C0 is replaced by the target monorepo's `.nexus/Containerfile` (exists, proven), and E0
is replaced by AllowAll at the service layer (wired). The motive plan predates the pilot; the pilot
proved the paths that the motive planned to build.

---

## Residual risks not cleared by the probe

The probe established that the capture passes the current free-space guard. The following risks
were not cleared and should be understood before running the full proof.

**Disk space margin is not wide.** The projected image (~12.8 GiB) fits within the 25–26 GB
80 % threshold, but the full proof also writes a 4 GiB artifact disk plus buildkit cache disks to
the same filesystem. The host at probe time was 93–94 % used. Half of the 12.8 GiB projection
comes from the ×2 headroom factor in `preflightCaptureSize`; if disk stays tight, revisiting that
factor is the first lever.

**`$TMPDIR` and the free-space guard.** The context image is written under `$TMPDIR`. If `TMPDIR`
points at a tmpfs, the free-space guard correctly measures RAM-backed capacity (it calls
`os.Statfs` on the actual write path) — a genuine improvement over the old fixed constant, but
worth stating explicitly rather than leaving implicit.

**The OOM-vs-disk swap.** The old `DefaultCaptureMaxBytes` constant guarded against host OOM from
page-cache pressure during large reads. The replacement guards disk exhaustion. The argument that
the OOM hazard is already designed away rests on capture staging being hardlink-based with
cross-device staging refused — this property is asserted by `TestFilteredWorktreeDir_CrossDeviceRefuses`
in `worktreedisk_test.go`, though that test self-skips when `/dev/shm` shares a device with the
temp dir; coverage is host-conditional, not unconditional on every machine.

---

## Cheapest probes to run before committing to the full proof

**Probe A — Source tree size after dockerignore** (COMPLETED 2026-08-14 — result: **PASS**):

Production `preflightCaptureSize` run in AUTO mode against the real tree. Effective capture:
**6.36 GiB**; projected ext4 image: **~12.8 GiB**; host free space: **31–33 GB** on `/`.
Guard threshold (80 % of free ≈ 25–26 GB) is not exceeded. Probe A does not need to be re-run
before the proof unless the host disk changes significantly (see "Disk space margin is not wide"
in Residual risks above).

**HISTORICAL — original probe commands (superseded by measured results)**:
```bash
cat <target-monorepo>/.dockerignore
du -sh <target-monorepo>/authgear-*-target 2>/dev/null
du -sh <target-monorepo>/.pnpm 2>/dev/null
# Estimate what WorktreeToDisk will capture:
# Total = 9.6 GB minus .git (du skips it) minus dockerignore-excluded dirs
# If the projected ext4 image would exceed 80% of free disk space, add the offending dirs to .dockerignore before proceeding
```

**Probe B — Host RAM headroom** (COMPLETED 2026-08-14 — result: **FAIL**):

Measured on the target host 2026-08-14:
- MemAvailable: **12.0 GiB** (12,580,592 kB) — pass condition **≥ 20 GB** — **FAILS**
- SwapFree: **~5 MB of 8.0 GiB** — swap **99.94% exhausted**, no elastic buffer
- One live `cloud-hypervisor` process running at time of measurement
- Host disk: 30.1 GiB available on `/`, **94% used**

Probe B does **not** pass at the original measurement. The verdict was NO-GO until MemAvailable reaches ≥ 20 GB and swap headroom is restored.

**Re-measurement 2026-08-14 (after docker services stopped)**:
- MemAvailable: **20.4 GiB / 21.9 GB** (21,370,464 kB) — **PASSES** the ≥ 20 GB condition
- SwapFree: **~1.3 GB of 8.0 GiB** — swap headroom restored
- The original FAIL measurement (12.0 GiB, swap 99.94% exhausted, one live `cloud-hypervisor` process) is retained above as the record of why the NO-GO was issued.
- **Operator decision required**: the pass condition is now met. Flipping the top-line verdict is the operator's call.

**HISTORICAL — original probe commands (superseded by measured results)**:
```bash
free -h
nexus3 --json sandbox list 2>/dev/null | jq -r '.[] | "\(.state)\t\(.project)/\(.name)"'
```
Pass condition: ≥ 20 GB free RAM on host (to allow governor head-room across the full 14-service
stack) AND no other large sandboxes still running. If the 2026-08-13 compose pilot sandbox is
still in `running` state, stop it first (after verifying it is not `paused` — if paused, resume
it first per GAP-7).

**Probe C — herdr binary + ABI check** (2 minutes):

```bash
herdr --version         # confirm ≥ 0.7.4
nexus3 __herdr-plugin abi  # should print "1"
```

If `herdr` is not found, the proof must be run via CLI directly (`nexus3 sandbox create --file`
+ `nexus3 exec`) which is operationally equivalent and proven.

---

## Recommended proof sequence

Once Probe B passes (Probe A complete — result: PASS, see GAP-1; Probe B was run 2026-08-14 and FAILED — re-run Probe B after reclaiming RAM to ≥ 20 GB free before attempting these steps):

```bash
# 1. Create the sandbox (kicks off builder VM image build; ~5 min)
nexus3 sandbox create compose/pilot-v2 --file <target-monorepo> --memory 8192

# 2. Start dockerd in-guest (must do this before compose)
nexus3 exec compose/pilot-v2 sh -lc nexus-dockerd-up

# 3. Minimal subset first (reproduces the prior pilot)
nexus3 exec compose/pilot-v2 sh -lc 'cd /workspace && docker compose up -d db redis api'

# 4. Check health
nexus3 exec compose/pilot-v2 sh -lc 'docker compose ps && curl -s http://localhost:8000/docs | head -5'

# 5. Authgear services (prerequisite for api/admin via network_mode: service:*)
nexus3 exec compose/pilot-v2 sh -lc 'cd /workspace && docker compose up -d authgear-teacher-student authgear-admin'

# 6. Add api/admin after authgear is healthy
nexus3 exec compose/pilot-v2 sh -lc 'cd /workspace && docker compose up -d api admin'

# 7. Frontend dev servers
nexus3 exec compose/pilot-v2 sh -lc 'cd /workspace && docker compose up -d student teacher storybook'

# 8. Port-forward for browser access (if desired)
nexus3 forward compose/pilot-v2 3000:3000 &  # student
nexus3 forward compose/pilot-v2 3001:3001 &  # teacher
```

*If the sandbox gets paused at any point (e.g., via herdr pane close):*
```bash
nexus3 sandbox resume compose/pilot-v2   # must do this before nexus3 sandbox stop
```

---

## Consciously accepted coverage gaps

The following are unit-test coverage gaps — not known bugs. Production code was verified correct at every site. They are recorded here so a future review does not re-litigate them.

**Exact-equality boundaries.** Mutating `total <= maxBytes` → `<` (the explicit-cap branch) and `projectedBytes <= safeAvail` → `<` (the auto-mode branch) both survive the suite. The guard is off-by-one at the exact equality boundary only; the real-world impact is negligible. Accepted.

**Error-message detail.** Mutating the contributor table's `topN = 5` → `1` survives the suite. The number of top contributors shown in the diagnostic message is untested. Cosmetic; accepted.

**`Bavail` vs `Bfree`.** The `statfsAvail` call that reads `Bavail` from `syscall.Statfs_t` is not unit-testable: `statfsAvail` is itself the stub seam, so its body is unreachable from unit tests by construction. Verifying that `Bavail` (usable by non-root) is used rather than `Bfree` (which includes root-reserved blocks) would require a real filesystem with reserved root blocks. Accepted as declared.

**`--file` capture call-site wiring.** A concurrent agent is attempting an entry-point test for the `WorktreeToDisk` call on the `--file` path in `cmd_sandbox.go`; that path may prove to require KVM. Outcome recorded separately — not asserted here.

**Walk-hardening asymmetry (`preflightCaptureSize` vs `filteredWorktreeDir`).** `preflightCaptureSize` was deliberately hardened to tolerate vanished entries (ENOENT) during the walk, so a live working tree with churning editor temp files does not hard-fail a capture. `filteredWorktreeDir` — the staging step that runs immediately afterwards — returns walk errors unconditionally, so the same race still hard-fails roughly ninety lines later. This is SAFE (it fails loudly; no undercount, no silent corruption) but the end-to-end robustness is narrower than the preflight's hardening suggests. Known limitation; not a bug.

---

## Relationship to doc 13

State machine diagrams (supervisor lifecycle, governor lifecycle) are in
[`docs/spec/13-state-machines.md`](13-state-machines.md). This document links to them rather
than restating them. The lifecycle trap in GAP-7 references section 1 of doc 13 ("Findings vs
doc 06: paused → stopped illegal"). The governor stall risk in GAP-3 references section 4
(governor loop; per-axis host guards: memory at `memory.go:330`, disk at `driver_resize.go:208`, vCPU unguarded — see GAP-3 above for exposure analysis).

---

*Document scope: `docs/spec/14-compose-monorepo-proof-gap-map.md`. The capture guard change that this document describes modified several `.go` files (replacing the `DefaultCaptureMaxBytes` constant with the free-space-derived guard in `worktreedisk.go` and `cmd_sandbox.go`).*
