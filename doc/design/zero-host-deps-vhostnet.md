# vhost-user-net backend (S9b-2)

Package `internal/core/driver/cloudhypervisor/vhostnet` is a pure-Go vhost-user
net **slave** for the cloud-hypervisor (CH) v53 master. It replaces the tap fd in
the netns child's frame pump so CH can run in an empty user+net namespace with
no tap, bridge, or `CAP_NET_ADMIN`. It is wired into the netns child by S9b-3 (below).

## API

`Serve(*net.UnixConn, Config) (*Device, error)` runs the protocol on an accepted
connection. `Device` is an `io.ReadWriteCloser` with datagram semantics:

- `Read` returns one frame the guest transmitted (virtio_net_hdr stripped).
  A too-small buffer yields `io.ErrShortBuffer` and drops the frame.
- `Write` delivers one frame to the guest, blocking until the guest posts an RX
  buffer. A frame larger than the posted chain returns an error and consumes
  nothing.
- Master disconnect: `Read` drains queued frames then returns `io.EOF`;
  `Write` returns `io.ErrClosedPipe`. Ring corruption ends the device and
  `Read` returns the error. `Close` releases mappings, fds and goroutines.

## Negotiated feature set

Offered by the slave (GET_FEATURES): `VIRTIO_F_VERSION_1` (bit 32),
`VIRTIO_NET_F_MAC` (bit 5), `VHOST_USER_F_PROTOCOL_FEATURES` (bit 30).

Rationale, from CH v53 `virtio-devices/src/vhost_user/net.rs`
(`Net::new`, `frontend_avail_features`):

- CH computes `acked = avail_features & backend_features`. Its `avail_features`
  contain `MRG_RXBUF`, `CTRL_VQ`, `GUEST_ANNOUNCE`, `RING_INDIRECT_DESC`,
  `RING_EVENT_IDX`, `IN_ORDER`, `ORDER_PLATFORM`, `NOTIFICATION_DATA`, plus CSUM/TSO/UFO when
  offload is on. Not offering a bit removes it from the guest. The slave
  therefore omits MRG_RXBUF (12-byte header only), CTRL_VQ (so STATUS and
  GUEST_ANNOUNCE are also never exposed and no control queue exists), EVENT_IDX
  (avoids the used_event/avail_event race surface), INDIRECT_DESC, and all
  offloads. Guests compute full checksums and never send GSO frames, so every
  frame is at most MTU + 14 bytes, which is what the userspace netstack wants.
- `VIRTIO_NET_F_MAC` is added by CH itself (`frontend_avail_features`) and
  masked out of the acked set sent to the backend (`activate`). Offering it is
  harmless and satisfies the slice contract; the MAC lives in CH's config space.
- One queue pair: RX = 0, TX = 1. MQ is not offered, so CH falls back to
  `DEFAULT_QUEUE_NUMBER = 2` and never sends GET_QUEUE_NUM.

Offered protocol features (GET_PROTOCOL_FEATURES): `REPLY_ACK` (bit 3),
`CONFIGURE_MEM_SLOTS` (bit 15).

- REPLY_ACK: after acking it CH sets NEED_REPLY on every request
  (`negotiate_features_vhost_user`, `set_hdr_flags`) and the `vhost` crate's
  `wait_for_ack` blocks for a u64 status. The slave replies 0 on success.
- CONFIGURE_MEM_SLOTS: without it CH answers memory hotplug (virtio-mem, the
  balloon-driven resize path nexus depends on) by re-sending the whole
  SET_MEM_TABLE (`add_memory_region_internal`). With it CH sends ADD_MEM_REG
  and the slave maps only the new region. GET_MAX_MEM_SLOTS returns 509.
- Not offered: MQ, LOG_SHMFD (dirty logging, live migration only),
  INFLIGHT_SHMFD (CH passes `inflight = None` unless negotiated), DEVICE_STATE,
  CONFIG (CH owns net config space), BACKEND_REQ (net passes
  `backend_req_handler = None`), RESET_DEVICE.

## Messages CH v53 sends for net

Source: CH v53.0 `virtio-devices/src/vhost_user/{net.rs,mod.rs,vu_common_ctrl.rs}`
calling `vhost` 0.16.0 `vhost_user/frontend.rs` (`Cargo.lock` at v53.0).

| Request (code) | Site in CH v53 | Notes |
| --- | --- | --- |
| SET_OWNER (3) | `negotiate_features_vhost_user`, `set_protocol_features_vhost_user` | idempotent |
| GET_FEATURES (1) | same | reply u64 |
| GET_PROTOCOL_FEATURES (15) | `negotiate_features_vhost_user` | only if PROTOCOL_FEATURES acked |
| SET_PROTOCOL_FEATURES (16) | same | no NEED_REPLY yet |
| SET_FEATURES (2) | `setup_vhost_user` | acked minus frontend-only bits |
| SET_MEM_TABLE (5) | `update_mem_table` | one memfd per region via SCM_RIGHTS |
| SET_VRING_NUM (8) | `setup_vhost_user` | power of two |
| SET_VRING_ADDR (9) | same | addresses are master-virtual; flags 0 |
| SET_VRING_BASE (10) | same | avail idx or restored base |
| SET_VRING_CALL (13) | same | eventfd |
| SET_VRING_KICK (12) | same | eventfd |
| SET_VRING_ENABLE (18) | `enable_vhost_user_vrings`, pause/resume | pause = enable 0, resume = enable 1 |
| GET_VRING_BASE (11) | `reset_vhost_user` | after enable 0; slave stops the ring |
| ADD_MEM_REG (37) | `VhostUserHandle::add_memory_region` | memory hotplug |
| REM_MEM_REG (38) | vhost crate API, handled | matched by gpa+size+uaddr |
| GET_MAX_MEM_SLOTS (36) | vhost crate, CONFIGURE_MEM_SLOTS | reply 509 |
| RESET_OWNER (4) | vhost crate API, handled | full reset |
| SET_VRING_ERR (14) | vhost crate API, fd closed and ignored | not sent by CH net |

Any other request closes the connection with a protocol error rather than
guessing at a reply layout.

## Ring processing

- Split virtqueues, sizes up to 32768 (power of two), 256 in CH by default.
- Memory: each region's memfd is `mmap`ed `MAP_SHARED` at its offset. Descriptor
  addresses are guest-physical and may span adjacent regions (copied piecewise).
  Ring addresses are master-virtual and must fall inside one region.
- Ordering: Go has no 16-bit atomics, so `avail.idx`, `used.idx`, and the flags
  words are accessed through their aligned enclosing 32-bit word with
  `sync/atomic` (CAS loop for stores). Go atomics are sequentially consistent:
  the used-entry write happens before the `used.idx` store (release), the
  `avail.idx` load precedes reading ring entries (acquire), and the
  `avail.flags` check for interrupt suppression follows the `used.idx` store
  with a full barrier. Little-endian hosts only (`Serve` refuses otherwise).
- A TX chain is consumed only after its frame has been copied out and handed to
  the reader, so stopping a ring mid-frame never loses a buffer.
- Interrupt suppression: `VRING_AVAIL_F_NO_INTERRUPT` in `avail.flags` is honoured.
  The slave never sets `VRING_USED_F_NO_NOTIFY`, so the guest always kicks.
- Malformed chains (address outside regions, range overflow, loop, next index
  beyond the ring, indirect descriptor, wrong direction, more than 1 MiB) are
  completed with length 0 and counted in `Stats.Malformed`; the device keeps
  running. Ring-level corruption (`avail.idx` further ahead than the ring size,
  head index out of range) is unrecoverable and ends the device.
- Memory table changes (SET_MEM_TABLE, ADD/REM_MEM_REG) stop both rings, swap
  the table, unmap dropped regions, and restart rings that were enabled. Ring
  position is preserved across the restart.

## Testing

`vhostnet` tests run an in-process fake master (memfd guest RAM, eventfd
kick/call, SCM_RIGHTS) following the CH activation sequence. `FuzzDecodeMessage`
covers the decoder. Goroutine leaks are checked with `runtime.NumGoroutine`
because `go.uber.org/goleak` is not in `go.mod`.

## Placement in the driver (S9b-3, decision D-1)

The vhost-user slave runs inside the existing re-exec'd
`CLONE_NEWUSER|CLONE_NEWNET` child, not in the supervisor.

Why the child and not the supervisor:

- The child outlives the supervisor. CH holds the vhost-user connection; if the
  slave lived in the supervisor, a supervisor crash or `supervisor-upgrade`
  would drop the guest NIC and the virtqueue state with it.
- Everything that already keeps the VM re-acquirable stays untouched: the
  socketpair to `PerimConn`, the netstack `AcceptVfkit` loop, the swappable
  pump, the control socket and `ReacquirePerimeter`. Only the tap fd that
  `tapPump` reads and writes is replaced (`vhostSlot` in
  `ch_netns_vhost_linux.go`).
- The socket lives in the 0700 control directory next to the control socket, so
  the same-uid boundary that protects the control token protects the NIC.

Mechanics:

- `StartNetnsRuntime` follows `Config.NetMode` only, never `NEXUS_NET_MODE`.
  Vhost-user adds `NEXUS_NETNS_NET_MODE=vhost-user` and
  `NEXUS_NETNS_VHOST_SOCKET`, and drops the tap/host-tap/bridge names.
- The child listens on the socket before it spawns CH, so CH's connect never
  races the listener. `vhostSlot` keeps accepting, so a master reconnect
  replaces the device without ending the pump. Frames written while no master
  is connected are dropped, like a tap with no reader.
- The child runs no `ip`, opens no `/dev/net/tun` and needs no `CAP_NET_ADMIN`.
  The live proof scrubs `ip` from `PATH`.
- `vm.create` sends `net[]={vhost_user:true, vhost_socket, mac, num_queues:2}`
  and `memory.shared=true` (CH refuses vhost-user devices on private memory).
- `NetnsIdentity`, `NetnsState` and the record gain `vhost_socket`
  (`omitempty`). `guest_tap_name` stays empty. Teardown never called
  `deleteTapBridge` on the netns path (the kernel drops the interfaces with the
  namespace); `Stop` now also removes the vhost socket and the control
  socket/token that a SIGKILLed child cannot remove.
- `CreateAndBoot` hands the recorded mode to the in-process boot driver through
  `driver.NetModeSetter`; before this the create-time boot silently used tap.
- `ForkFrom` refuses in vhost-user mode until S9b-6 rewrites the socket path.

## Findings

- **CH JSON field name.** The `NetConfig` field is `vhost_socket`, not
  `socket` (`socket` is the CLI key). With `socket`, `vm.boot` fails with
  `No socket provided when using vhost-user`. S9b-1 had the wrong tag.
- **Device creation time.** CH connects to the socket at `vm.boot`, not at
  `vm.create`, so the listener only has to exist by then.
- **Guest side.** With no MRG_RXBUF and no offloads, the guest kernel posts
  ordinary 1526-byte RX buffers; DHCP, ICMP to the gateway, DNS and TCP fetches
  through the netstack all work with the 12-byte header.
- **Backpressure.** `Device.Write` blocks until the guest posts an RX buffer,
  so a stalled or paused guest blocks the pump's host-to-guest direction where
  a tap would drop. The netstack sender absorbs this in its datagram socket
  buffer. Worth measuring in S9b-7.
- **Teardown timing.** After `Stop`, the group can linger briefly as a zombie
  until init reaps it; tests poll `kill(-pgid, 0)` (`waitForGroupExit`).

## Proof

`TestVhostNet_GuestNetworking` (integration tag) boots CH v53 with `PATH`
pointing at an empty directory. It appends a probe `/init` to the Alpine
initramfs and reads the serial log for: DHCP lease `192.168.127.2/24`, ping to
`192.168.127.1`, a fetch from an allowed IP, and DNS plus a fetch by name
(needs a host-resolvable `example.com`; otherwise not asserted). It also asserts
that the child and CH network namespaces differ from the host's, that `Stop`
removes the group, socket and control files, and that the test process leaks
no fds.

Run it with:

    TMPDIR=/var/tmp make test-integration GOTEST_PKGS=./internal/core/driver/cloudhypervisor/ GOTEST_ARGS='-run TestVhostNet'

## Snapshot, restore and fork (S9b-6)

- **Mode comes from the snapshot.** `snapshotNetBackend` reads `config.json`
  `net[0]`: `vhost_user: true` is vhost-user, anything else is tap. `ForkFrom`
  hands `StartNetnsRuntime` a config copy with that mode, so a tap snapshot
  restores as tap whatever `NEXUS_NET_MODE` (or `d.cfg.NetMode`) says.
  `Service.Fork` and `RestoreFromSnapshot` cross-check the snapshot mode
  against the origin record (`SnapshotNetModer`), refuse on mismatch, and stamp
  the same `NetMode` on the child record. Tap children keep `net_mode` absent.
- **Socket rewrite.** The child restores from a per-child `config.json` whose
  `net[0].vhost_socket` is the child's own `vhost-<childID>.sock` in the netns
  control dir; the guest MAC is unchanged (it lives in guest memory), so
  sibling clones share MAC and IP and are separated by netns, not by address.
  `RestoreFromSnapshot` now persists the netns identity (`VhostSocket`
  included) like `Fork` does; before, restore children had none.
- **Listener before restore.** The netns child starts the vhost slot before it
  spawns CH and only then issues `vm.restore`, so CH always finds the socket.
- **Base index.** CH restores each ring's base from the guest's avail index.
  For RX that skips every buffer the guest posted but the slave had not
  consumed (here 253 of 256), and the restored guest got no replies. The slave
  completes every buffer it consumes at once, so `used.idx` is the true
  position; `startLocked` now resumes there and ignores the master's base.
- **Prefault.** `vm.restore` used `prefault=true`, which commits the whole
  guest RAM for a memfd-backed guest: 4 GiB RSS for a child whose parent held
  470 MiB after ballooning, and a 10 s restore timeout under host swap
  pressure. Shared-memory snapshots now restore with `prefault=false`
  (`restorePrefault`); non-shared snapshots are unchanged. There is no uffd
  or ondemand restore path in the driver, so nothing else needs disabling.
  Restored child RSS: 210 MiB, restore 1.6 s.
- **One data point** (alpine, 512 MiB boot / 1024 MiB max, 200 MiB written,
  same host): snapshot of a tap sandbox 1.63 s, 1025 MiB on disk; of a
  vhost-user sandbox (shared memfd) 1.19 s, 212 MiB on disk (1025 MiB
  apparent, sparse). At the default 4 GiB ceiling: tap 8.1 GiB on disk,
  vhost-user 205 MiB.
- **Live** (vhost-user, `--egress closed --repo octocat/hello-world
  --allow-host example.com`): restore and fork x2 children each had a working
  DNS, `example.com` 200 and `google.com` blocked; one child sent ~1490 frames
  to random addresses while its sibling's `eth0` rx counter stayed unchanged.
  A sandbox with a live `--mount` is refused by the existing fork and snapshot
  mount guard (D-PD-53), as for tap.

## Not covered yet

- Throughput against tap and the soak run (S9b-7).

## Known gap under `kernel.apparmor_restrict_unprivileged_userns=1` (S9b-8, S9c)

Single-file `--mount` re-execs with `CLONE_NEWUSER|CLONE_NEWNS`, which needs
`CAP_SYS_ADMIN` in the new user namespace. With the restriction set to 1 the
unprivileged userns loses its capabilities, so single-file mounts are expected
to fail there in both net modes. Directory mounts do not use that path.
Tracked for S9c.

Builder VMs follow the same rule as sandboxes: `NEXUS_NET_MODE` is read once at
`create --file` and passed to the detached builder supervisor as `--net-mode`,
so the build runs vhost-user with no tap or bridge. A tap failure with EPERM now
names `NEXUS_NET_MODE=vhost-user` and the sysctl. `scripts/s9b-restricted.sh`
runs the end-to-end check once the sysctl is 1.
