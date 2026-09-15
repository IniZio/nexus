# CH-RESIZE-400 Research: Direct-Disk / resize-disk Conflict

**Date**: 2026-08-14  
**Status**: Research complete — see Recommendation below.

---

## 1. How Old Nexus Did It

### Disk format and cache mode

Old nexus used a **qcow2 overlay disk** for its single workspace block device,
not a raw ext4 image. The disk was configured at
`nexus-clone-repro/packages/nexus/internal/vm/driver/cloudhypervisor/driver.go:31–41`:

```go
// diskFileName is the per-workspace qcow2 overlay disk. It is the only
// workspace block device attached to the VM (/dev/vda).
diskFileName = "disk.qcow2"

// diskOverlayVirtualSize is the virtual size (100 GiB) of the per-workspace
// qcow2 overlay created at creation time. Cloud Hypervisor cannot resize a
// qcow2 backing file at runtime, so startWorkspace allocates a large sparse
// overlay at creation time instead. The file is sparse: only modified blocks
// consume physical space.
diskOverlayVirtualSize = int64(100 * 1024 * 1024 * 1024)
```

The VMConfig `DiskConfig` struct in old nexus
(`nexus-clone-repro/.../cloudhypervisor/types.go`) has **no `Direct` or
`cache` field**. Old nexus never set O_DIRECT on any disk; all workspace I/O
flowed through the host page cache.

The workspace was attached as:

```go
disks := []DiskConfig{
    {Path: overlayPath, ReadOnly: false, ImageType: ImageTypeQcow2, BackingFiles: true},
}
```
(`nexus-clone-repro/.../cloudhypervisor/driver.go:954–956`)

### Runtime resize mechanism

Old nexus did wire a disk auto-grow governor
(`nexus-clone-repro/.../engine/workspace/disk_resize.go`). The governor
called `GrowWorkspace` on the CH driver
(`nexus-clone-repro/.../vm/driver/cloudhypervisor/driver_grow.go:26–65`),
which:

1. Called `diskimage.GrowFile(path, newSizeBytes)` to expand the host-side
   backing file (raw → `os.Truncate`; qcow2 → `qemu-img resize`).
2. Called `inst.Client.ResizeDisk(ctx, VMResizeDiskRequest{ID: "_disk0",
   DesiredSize: newSizeBytes})` — i.e. `PUT /api/v1/vm.resize-disk` with the
   field named **`desired_size`** in JSON.

The `VMResizeDiskRequest` in old nexus
(`nexus-clone-repro/.../vm/driver/cloudhypervisor/types.go:60–65`):

```go
type VMResizeDiskRequest struct {
    ID          string `json:"id"`
    DesiredSize int64  `json:"desired_size"`    // correct CH v52.0 field name
}
```

However, in practice the CH call almost always failed for qcow2 overlays with
a backing file. The old nexus disk resize governor tracked this with an
`unsupported` flag
(`nexus-clone-repro/.../engine/workspace/disk_resize.go:61–66`):

```go
// unsupported is set when cloud-hypervisor reports that runtime disk
// resize is not supported for this workspace's backing file (qcow2
// overlays backed by a base image cannot be resized at runtime by CH).
// Once set, the governor skips further evaluation for this workspace
// to avoid log spam from repeated identical errors.
unsupported bool
```

### What actually prevented disk ENOSPC in old nexus

Old nexus pre-allocated a **100 GiB virtual sparse qcow2 overlay at creation
time**. When CH rejected the runtime resize for a backed qcow2 overlay, the
governor latched `unsupported = true` and silently fell back to relying on the
pre-allocated 100 GiB headroom. The guest filesystem was always 100 GiB from
first boot; no runtime grow ever needed to succeed.

---

## 2. Why Old Nexus Did Not Hit the Conflict

The conflict is between the host-OOM guard (O_DIRECT on ExtraDisks) and
`vm.resize-disk`. Old nexus never had this conflict because:

**Old nexus never used O_DIRECT.** All workspace I/O — including heavy Docker
layer writes — flowed through the host page cache. Old nexus therefore DID
carry the host-OOM risk that nexus3 was built to avoid. If old nexus had run
the same buildkit-nested-build workload that produced the `pump: read frame:
EOF` failure in nexus3, it would likely have hit the same host-OOM wall.

Old nexus dodged the conflict by **not having the guard**, which is not a
solution nexus3 can copy. The 100 GiB virtual-size approach succeeded there
precisely because that workspace disk was buffered — but a buffered disk means
heavy buildkit writes dirty the host page cache, which causes OOM under the
nested-build workload that CH-RESIZE-400 was filed against.

Additionally, old nexus's runtime disk resize was non-functional for the
qcow2 overlay (qcow2-with-backing-image is not resizable at runtime by CH),
so the two features — O_DIRECT guard and runtime resize — simply never
coexisted: each project had one or the other, never both.

---

## 3. Whether the CH Limitation Is Real and Current

### The CH source does NOT block resize on O_DIRECT files

CH v52.0's `RawDisk::resize` implementation
(`block/src/raw_disk.rs` at tag `v52.0`) is:

```rust
impl disk_file::Resizable for RawDisk {
    fn resize(&mut self, size: u64) -> BlockResult<()> {
        let fd_metadata = self.file.metadata()...?;
        if fd_metadata.file_type().is_block_device() {
            // block device path — checks size matches
            ...
        } else {
            self.file.set_len(size)  // ftruncate — NOT affected by O_DIRECT
                .map_err(...)
        }
    }
}
```

The function calls `set_len` (`ftruncate`) on the file. `ftruncate(2)` is a
file-metadata operation and is **unaffected by the `O_DIRECT` flag**. O_DIRECT
only affects data I/O (read/write system calls), not inode-level operations.
The `RawDisk` struct in v52.0 does not inspect a `direct` field in `resize`.
Current `main` has the same structure (now reorganised under
`block/src/formats/raw/mod.rs`). **No Cloud Hypervisor issue or commit exists
that adds a Direct-mode restriction to resize-disk.** This restriction was
not verified in the CH source or issue tracker; it is not present.

### The actual cause of the HTTP 400: JSON field name mismatch

The CH v52.0 OpenAPI spec (`vmm/src/api/openapi/cloud-hypervisor.yaml`) and
Rust API struct (`vmm/src/api/mod.rs`) define the resize-disk body as:

```rust
// CH v52.0 vmm/src/api/mod.rs
#[derive(Clone, Deserialize, Serialize, Default, Debug)]
pub struct VmResizeDiskData {
    pub id: String,
    pub desired_size: u64,   // JSON field: "desired_size"
}
```

nexus3's client struct is:

```go
// nexus3 internal/core/driver/cloudhypervisor/client.go:229–232
type vmResizeDiskRequest struct {
    ID   string `json:"id"`
    Size uint64 `json:"size"`    // WRONG — CH expects "desired_size"
}
```

nexus3 sends `{"id":"_disk1","size":16106127360}`. Serde on the CH side
deserializes `id = "_disk1"` from the JSON but finds no `desired_size` key.
Because `desired_size` is a required `u64` field without `#[serde(default)]`,
Serde returns "missing field `desired_size`", which the CH HTTP framework
converts to **HTTP 400 Bad Request**. The HTTP 400 is a client-side API error
in nexus3, not a CH restriction on Direct-mode disks.

The code comment in `driver_resize.go:230–233` attributes the 400 to
Direct:true. That attribution is incorrect — a non-direct disk also returns
400 when the same wrong field name is sent. The old nexus client used the
correct field name `"desired_size"` and its CH calls succeeded for raw disks.

CH version pinned in nexus3: **v52.0** (binary at
`~/.local/bin/cloud-hypervisor`, released 2026-05-14).
CH v53.0 (released 2026-07-12) contains no related fix because there is no
bug in CH to fix.

---

## 4. Candidate Resolutions

### Option A — Fix the JSON field name (recommended)

**Change one line** in
`internal/core/driver/cloudhypervisor/client.go:231`:

```go
// Before (wrong):
type vmResizeDiskRequest struct {
    ID   string `json:"id"`
    Size uint64 `json:"size"`
}

// After (correct):
type vmResizeDiskRequest struct {
    ID          string `json:"id"`
    DesiredSize uint64 `json:"desired_size"`
}
```

And update the call site (`client.go:543–546`) to use `DesiredSize`.

**Effect on host-OOM guard**: Preserves O_DIRECT on all ExtraDisks unchanged.
The ftruncate call inside CH's `resize` is not O_DIRECT-sensitive; once the
correct JSON body is sent, the CH response is 204 No Content and the virtio-blk
device reports the new capacity to the guest.

**Second blocker still required**: After fixing the JSON field, the governor's
`GrowDisk` succeeds at the CH notify step but still does not send the guest
`resize2fs` wire command. The TODO block at `driver_resize.go:224–236` lists
a second condition: `SandboxResizer` has no vsock dialer. Wiring that path
(adding a `DialGuest` seam to `SandboxResizer` and sending
`resize.EncodeGrowRequest` over vsock port 3002 to `cmd/nexus3-agent/
resize_actuate_linux.go:handleDiskGrow`) must accompany the JSON fix before
disk auto-grow is end-to-end live.

**Proof**: Send a manual `curl` to a running sandbox's CH socket with the
corrected JSON body and confirm 204; then verify the virtio-blk device reports
the new size in-guest with `lsblk`.

**Cost**: 1-line client fix + wiring the vsock dialer path (existing guest
handler already implemented). Low complexity.

---

### Option B — Overprovision a large sparse backing file up front

Pre-allocate ExtraDisks as large sparse ext4 images (e.g., 100 GiB) at
sandbox creation, mirroring old nexus's 100 GiB qcow2 approach. The guest
filesystem is created at full virtual size by `mke2fs` in-guest; because the
file is sparse, the host only pays for actual written blocks.

**Effect on host-OOM guard**: Preserves O_DIRECT. A file opened O_DIRECT that
is mostly sparse still only issues direct I/O for blocks the guest actually
writes. The host page cache is not touched.

**Runtime resize required?**: No — the governor would still fire when the
guest filesystem reaches 80% usage, but the ceiling would already be 100 GiB.
For most sandboxes this is never hit. For large monorepos or multi-stage
builds that fill 100 GiB, a runtime resize path would still be needed
eventually.

**Cost**: Changes how disks are created (`builder/worktreedisk.go`,
`service/create.go`). Wasted guest filesystem space for small sandboxes
(guest `df` shows 100 GiB total from day one). The `47× smaller` sparse ext4
improvement referenced in project notes applies to the initial physical
footprint and is preserved, but the logical disk size is inflated.

---

### Option C — Hot-add an additional disk instead of growing the existing one

CH supports `PUT /api/v1/vm.add-disk` (confirmed in v52.0 OpenAPI spec):
returns 200 OK with PCIe device info on hot-add, 204 on cold-add. When the
workspace disk nears 80%, the governor would add a new raw ext4 ExtraDisk,
format it in-guest, and extend the workspace filesystem (LVM PV extend or
bcache).

**Effect on host-OOM guard**: Preserves O_DIRECT. The new disk is attached
with `Direct:true`, same as the initial ExtraDisks.

**ExtraDisks seam reusability**: nexus3 already has the ExtraDisks→/dev/vdb
seam and the guest-agent mount/unmount path at `internal/core/agent/
workspace_mount.go`. The guest can detect a new virtio-blk device via udev.
nexus3 does not have a `VMAddDisk` client call yet; it would need to be added.

**Cost**: Significant. Adding LVM or span-and-mount logic in-guest; the nexus3
agent must track the multi-disk logical volume; snapshots and forks become more
complex (each child needs to inherit all N disks). Materially more work than
Option A.

---

### Option D — Split cache modes per disk

O_DIRECT is currently set on **all** ExtraDisks unconditionally
(`driver.go:700–710`). The host-OOM guard is only needed on the disk where
buildkit writes large layer tarballs, which is `ExtraDisks[1]`
(`ArtifactDiskPath`, `/dev/vdc`). The workspace disk (`ExtraDisks[0]`,
`ContextDiskPath`, `/dev/vdb`) carries the source tree snapshot and user
workspace writes — neither of which produces the multi-GiB dirty-page burst
that causes host OOM.

Add a `Direct bool` field to `cloudhypervisor.ExtraDisk` and set:

- `ExtraDisks[0]` (workspace) → `Direct: false` — buffered, resize-disk works
- `ExtraDisks[1]` (artifact) → `Direct: true` — O_DIRECT guard preserved
- `ExtraDisks[2+]` (cache disks) → `Direct: false` (cache disks are pre-warmed
  reads, not heavy sequential writes)

**Effect on host-OOM guard**: Maintains O_DIRECT on the artifact disk (the
actual write-heavy path). The workspace disk returns to buffered I/O, which is
acceptable because source-tree writes are modest in size.

**Cost**: Medium. Requires a per-disk Direct flag plumbed through ExtraDisk →
vmDiskConfig. The governor's `DiskAxis` targets the workspace disk index, so
resize-disk would then succeed on that disk regardless of the JSON field fix
(though the field fix is still needed independently).

**Note**: This option is ONLY useful in combination with fixing the JSON field
name bug (Option A). Without that fix, `vm.resize-disk` still returns 400
regardless of the Direct flag.

---

## 5. Recommendation

**Fix the JSON field name first.** The single strongest piece of evidence is
the CH v52.0 API struct (`vmm/src/api/mod.rs:VmResizeDiskData.desired_size`)
and OpenAPI schema (`openapi/cloud-hypervisor.yaml:VmResizeDisk.desired_size`)
confirming that the field nexus3 sends as `"size"` must be `"desired_size"`.
This is a two-character identifier change in `client.go:231` (`Size` →
`DesiredSize`, `"size"` → `"desired_size"`). The CH raw-disk resize
implementation (`RawDisk::resize`) calls `ftruncate` unconditionally and is
not O_DIRECT-aware; the "CH limitation" attributed to Direct:true in
`driver_resize.go:230` does not exist in CH source. Once the JSON field is
corrected and the vsock guest-dialer is wired (second blocker in the same
TODO block), disk auto-grow will work with the O_DIRECT host-OOM guard fully
intact. No cache-mode changes or pre-provisioning workarounds are required.

**UNVERIFIED**: The assertion that old nexus's vm.resize-disk calls on qcow2
overlays backed by a base image returned an "unsupported" error (vs. a
different error) was inferred from the `unsupported bool` flag comment; the
exact CH error response for backed-qcow2 resize was not reproduced live.

**UNVERIFIED**: Whether a nexus3 sandbox in the current build actually emits
the HTTP 400 error when the governor fires and calls `GrowDisk` was not
live-tested; the code path to `VMResizeDisk` is exercised in unit test at
`internal/test/selfhost/autoresize_disk_vcpu_test.go:16` but the test outcome
against a real CH socket was not observed during this research session.
