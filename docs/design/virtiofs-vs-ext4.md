# virtiofs vs ext4 virtio-blk — spike findings (D-DC-09)

**Date**: 2026-08-18  
**Branch**: milestone-a-agent-sandbox  
**Decision**: D-DC-09 — ext4 virtio-blk chosen as the in-guest workspace filesystem  
**Status**: spike complete; decision ratified. The raw bench output, the bench script, and the guest benchmark source are all committed alongside this document at `docs/design/bench-data/` — that is the authoritative, self-contained evidence for D-DC-09. Caution for auditors: commit `8f3e8fc1c7306ed5d1c4cf2a9dc698f9ff2b1037` also carries `spike/virtiofs/last_run.bench_lines.txt`, an EARLIER 3-round run with different figures and no git-status series; it is NOT the source of the numbers below. This change set untracks `spike/`, which is why the real evidence is committed here instead.

---

## Why this was measured

`/workspace` inside the builder VM is the working-tree mount point used by every
build. The original plan was virtiofs (shared host directory via the virtio-fs
device). virtiofs would have kept the host working tree live-mounted inside the
VM, but the metadata cost of every `git status`, `find`, and file-open over
virtio-fs on a warm path needed to be measured before committing to it.

---

## Method

Two filesystems were benchmarked in-guest under the same KVM sandbox:

| Filesystem | Device | Mount options |
|---|---|---|
| `virtiofs` | virtio-fs device (host-shared dir) | `always-writeback` cache mode |
| `ext4-blk` | virtio-blk device (sparse ext4 image) | defaults |

A Go benchmark binary (`docs/design/bench-data/guest_bench/main.go`) ran 10 rounds of
1 000-file create / stat / unlink. A separate `git status` probe ran 10 warm
rounds (plus one cold round) on a 19-file repository cloned onto each
filesystem.

Both filesystems were initialised with `cp -a` from the same source tree to
equalise the directory structure. (An earlier run using `mke2fs -d` to
pre-populate the ext4 image was voided — that approach leaves inode allocation
artifacts that distort metadata timing. Only the `cp -a`-equalised run is
reported here.)

---

## Results — metadata operations (1 000 files, 10 rounds each)

| Metric | virtiofs (avg) | ext4-blk (avg) | Ratio |
|---|---|---|---|
| create | 148 ms | 9.0 ms | **16.4×** |
| stat | 32.8 ms | 0.48 ms | **~68×** |
| unlink | 98.4 ms | 6.8 ms | **14.4×** |
| **total** | **279.3 ms** | **16.3 ms** | **~17.1×** |

All figures in this table are the **always-writeback** run (see Method above). The `auto`-mode
run measures **~12.8×** on the same total; `auto` is what production uses — see
`docs/site/security/accepted-risks.md`.

Raw round data (ns) from `docs/design/bench-data/2026-08-18-gitstatus-redo-always-writeback-bench_lines.txt` (10 rounds, `cp -a`-equalised legs):

**virtiofs create (ns):** 163 950 336 / 142 799 009 / 125 261 024 / 126 244 787 /
130 654 252 / 148 820 047 / 171 842 650 / 153 476 152 / 149 077 657 / 168 613 434

**ext4-blk create (ns):** 11 657 148 / 8 826 484 / 8 168 930 / 8 576 650 /
8 596 496 / 8 256 363 / 7 824 688 / 8 519 633 / 11 732 148 / 8 203 574

---

## Results — git status (19-file repo, 10 warm rounds + 1 cold)

| Condition | virtiofs | ext4-blk | Ratio |
|---|---|---|---|
| Cold (first run) | 2 367 ms | 1 744 ms | 1.4× |
| Warm avg (r1–r10) | **156.3 ms** | **34.5 ms** | **~4.5×** |

Warm round values (ms):

**virtiofs:** 144 / 164 / 146 / 143 / 149 / 190 / 199 / 125 / 160 / 142  
**ext4:** 30 / 33 / 40 / 26 / 27 / 45 / 36 / 49 / 29 / 30

---

## Conclusion and architectural decision

virtiofs incurs a **~17× metadata penalty** for file create/stat/unlink and a
**~4.5× warm penalty** on `git status`, measured in-guest against the same
KVM host. For a builder VM whose primary workload is source compilation and
`git` operations, this overhead is unacceptable.

**D-DC-09 verdict: use ext4 virtio-blk for `/workspace`.**

The working tree is captured to a sparse ext4 image at sandbox-create time
(`nexus3 sandbox create`) and mounted as a virtio-blk device inside the VM.
Live host-directory sharing via virtiofs is not used.

Named volumes (D-PD-82) absorb the write-heavy paths for `node_modules` and
similar caches, where virtiofs write amplification would have been most visible.
Because herdr/orca VMs never fork a running instance, the host-mount-hostility
of virtiofs costs the primary agent-launch flow nothing.

**Reference in production source:**  
`internal/core/builder/builderimage/bootlayers.go` — comment on `/workspace`
directory creation (`addBootLayers`, items 4 and the function docstring).
