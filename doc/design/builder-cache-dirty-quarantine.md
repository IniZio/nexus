# Builder cache slot: quarantine on unclean death (reverses the e2fsck-reuse decision)

## Decision

A buildkit (or any ecosystem) cache-disk slot found fenced dirty is never
reused. `ensureCacheDiskAt` renames the image to `<image>.quarantine-<unix>`,
removes the `.dirty` marker, deletes older `.quarantine-*` copies of the same
slot (at most one is kept), and creates a fresh empty image at the preserved
size. It logs:

    cachedisk: <key> slot N left dirty by a prior unclean death; quarantined to <path>, starting with an empty cache

The `.lock` sidecar is never moved (slot lease rule: do not unlink the lease file).

## Provenance of the reversed decision

The e2fsck-reuse branch was decision C of motive `builder-oom-crashloop`
(2026-09-18, `.groundwork/motives/builder-oom-crashloop/motive.md`; annotated
there as reversed). Code comments and tests labelled it "D-DC-31", but that id
is the unrelated OCI boot-config inheritance fix in `.groundwork/BACKLOG.md`;
the label was a mis-reference.

## Concurrency

`ensureCacheDiskAt` runs only while the caller holds the slot flock lease
(`leaseCacheDiskSlot`), so a slot leased by a live builder is skipped as busy
and never quarantined. Residual (see `internal/supervisor/cachedisk_lease.go`):
if a supervisor died and an orphan VM still holds the image, the slot reads
free; quarantine renames the inode, so the orphan keeps writing the quarantined
file and the new build gets a fresh image. No shared corruption.

## Why the earlier "e2fsck then reuse" branch was wrong

The previous behaviour ran `e2fsck -f -p` on a dirty slot and reused it when
the exit code was 0 or 1. e2fsck verifies ext4 metadata and replays the journal.
It cannot tell whether file data written by buildkitd was flushed. The cache
ext4 uses ordered-data journaling, so after a SIGKILL of the VM, committed
buildkit snapshots can contain zero-length files whose inodes are valid.
Metadata check is not data integrity.

Observed in production (agentic-artifacts): `lib/ld-musl-x86_64.so.1` Size 0 in
snapshots 11 and 14, so every later alpine `RUN` failed with runc exit 255
until `caches/buildkit*.ext4*` was deleted by hand. Same class on the
overlayfs snapshotter: "rootfs export is hollow: 2467/2562 regular files (96%)
are zero-length". This is the poisoned-snapshot class in
`.groundwork/motives/nexus3-buildkit-poisoned-snapshot-on-vm-death/` (TBD-1
listed "fence the cache disk with a dirty marker that forces a wipe" as the
only option covering a SIGKILLed guest).

## Cost

An unclean death costs one cold build for that slot. The quarantined copy is
kept (one per slot) for forensics and can be deleted by hand.

## What still leaves a slot dirty

The marker is cleared only when the guest sync exec returns 0, the VMM stops,
and the caller context was not cancelled (`vmbuilder.go`, step 3.5, decision
D-4). A `SIGTERM`/`SIGINT` or `NEXUS_BUILD_TASK_TIMEOUT` expiry therefore
shuts the VM down with a sync but still leaves the slot dirty, so the next build
quarantines it.

## Not done

Making buildkitd commit durably (fdatasync before boltdb commit) has no clean
hook: buildkitd is an unmodified upstream binary. The agent already calls
`syscall.Sync()` on the success path (`builder_role_linux.go`).
