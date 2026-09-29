# vhost-user-net backend (S9b-2)

Package `internal/core/driver/cloudhypervisor/vhostnet` is a pure-Go vhost-user
net **slave** for the cloud-hypervisor (CH) v53 master. It replaces the tap fd in
the netns child's frame pump so CH can run in an empty user+net namespace with
no tap, bridge, or `CAP_NET_ADMIN`. It is not yet wired into the driver (S9b-3).

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

## Open items for S9b-3

- Real CH v53 integration (DHCP, ping, fetch) is not covered by this slice.
- Restore/ondemand (uffd) with shared memory is S9b-6.
