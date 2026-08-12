# Pinned Guest Kernel — x86_64

## Shipped artifact

| Field        | Value |
|--------------|-------|
| File         | `images/kernel/vmlinux-x86_64` |
| Architecture | x86_64 |
| Format       | uncompressed ELF vmlinux (PVH-bootable by Cloud Hypervisor) |
| Version      | Linux **6.12.76** |
| Size         | ~27 MB (stripped) |
| SHA-256      | `ebc744eded3ccc69c404b9cd3c380f28b127b04b2741cc90f2c84aad687d975d` |

## Provenance

Built from upstream Linux 6.12.76 source using `scripts/kernel/build.sh`.
This is a **config-reproducible from-source artifact** — the source tarball and kernel
config are pinned. However, it is **NOT byte-for-byte reproducible**: the kernel ELF embeds
a build timestamp and BuildID, so a fresh build produces a different sha256. Achieving
byte-identical output additionally requires a pinned toolchain and
`SOURCE_DATE_EPOCH`/`KBUILD_BUILD_TIMESTAMP` (known future work — not set in `build.sh`).

### Upstream tarball

| Field     | Value |
|-----------|-------|
| URL       | `https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-6.12.76.tar.xz` |
| SHA-256   | `bbb43e834c46e6bd49a5c28f22e679a937443404e1f653204d4b24929f3ad896` |

### Reproducing the build

```sh
# Install toolchain (Debian/Ubuntu):
# apt-get install build-essential libncurses-dev bison flex libssl-dev bc libelf-dev binutils

# Build (uses committed scripts/kernel/config-6.12.76 — no network fetch needed):
bash scripts/kernel/build.sh

# Output: images/kernel/vmlinux-x86_64
# Verify:
sha256sum images/kernel/vmlinux-x86_64
# sha256 of the artifact shipped in this commit (identity/provenance record).
# A rebuild will produce a DIFFERENT hash — see reproducibility note above.
# Shipped digest: ebc744eded3ccc69c404b9cd3c380f28b127b04b2741cc90f2c84aad687d975d
```

The build uses the committed config at `scripts/kernel/config-6.12.76` (normalized via
`make olddefconfig` from libkrunfw's x86_64 base + the overlay in `build.sh`).
Fetching libkrunfw is a one-time bootstrap step documented in `build.sh` but not required
for rebuilds once `config-6.12.76` is committed.

### Build environment (used for this artifact)

- Host: Linux 7.0.0-28-generic x86_64
- Toolchain: gcc 13.3.0 (Ubuntu 13.3.0-6ubuntu2~24.04.1), GNU ld 2.42
- Build date: 2026-08-12
- Duration: ~126 seconds (12-core host)

### Digest verification

```
sha256sum images/kernel/vmlinux-x86_64
# ebc744eded3ccc69c404b9cd3c380f28b127b04b2741cc90f2c84aad687d975d  images/kernel/vmlinux-x86_64
```

## Required kernel config

The shipped artifact satisfies all requirements. Key configs compiled **built-in** (`=y`):

### Cloud Hypervisor / virtio transport

| Option                      | Value | Rationale |
|-----------------------------|-------|-----------|
| `CONFIG_VIRTIO_PCI`         | `y`   | PCI transport for all virtio devices under Cloud Hypervisor |
| `CONFIG_VIRTIO_BLK`         | `y`   | Block device for root disk (virtio-blk) |
| `CONFIG_VIRTIO_NET`         | `y`   | Network device (virtio-net) |
| `CONFIG_VIRTIO_CONSOLE`     | `y`   | Serial console (virtio-console) |
| `CONFIG_VIRTIO_FS`          | `y`   | virtiofs mount for shared workspace directories |
| `CONFIG_VIRTIO_VSOCK`       | `y`   | guest↔host vsock channel (agent comms) |
| `CONFIG_VSOCKETS`           | `y`   | AF_VSOCK socket family (prerequisite for vsock) |
| `CONFIG_PVH`                | `y`   | PVH entry point (required by Cloud Hypervisor ELF load) |
| `CONFIG_ACPI`               | `y`   | ACPI for PCI enumeration |
| `CONFIG_SERIAL_8250`        | `y`   | 8250 UART serial console |

### Docker / bridge / netfilter

| Option                         | Value | Rationale |
|--------------------------------|-------|-----------|
| `CONFIG_BRIDGE`                | `y`   | Linux bridge (docker bridge networking) |
| `CONFIG_BRIDGE_NETFILTER`      | `y`   | Bridge ↔ netfilter integration |
| `CONFIG_NETFILTER`             | `y`   | Netfilter core |
| `CONFIG_NF_CONNTRACK`          | `y`   | Connection tracking |
| `CONFIG_NF_NAT`                | `y`   | NAT framework |
| `CONFIG_IP_NF_IPTABLES`        | `y`   | iptables IPv4 |
| `CONFIG_IP_NF_FILTER`          | `y`   | iptables filter table |
| `CONFIG_IP_NF_NAT`             | `y`   | iptables NAT table |
| `CONFIG_IP_NF_TARGET_MASQUERADE` | `y` | MASQUERADE target |
| `CONFIG_NF_TABLES`             | `y`   | nftables |
| `CONFIG_NFT_NAT`               | `y`   | nftables NAT expression |
| `CONFIG_NFT_MASQ`              | `y`   | nftables masquerade |
| `CONFIG_NFT_FIB`               | `y`   | nftables FIB (required by netavark >= 1.14) |
| `CONFIG_IP_VS`                 | `y`   | IPVS (kube-proxy / docker setups) |
| `CONFIG_DUMMY`                 | `y`   | Dummy network device |
| `CONFIG_IP_FORWARD`            | `y`   | IP forwarding |

### Additional subsystems

| Option                  | Value | Rationale |
|-------------------------|-------|-----------|
| `CONFIG_OVERLAY_FS`     | `y`   | overlayfs for container layers |
| `CONFIG_VETH`           | `y`   | veth pairs for container networking |
| `CONFIG_BLK_DEV_INITRD` | `y`   | initramfs support (future-proofs initramfs boot) |
| `CONFIG_PSI`            | `y`   | Pressure Stall Information (memory governor) |
| `CONFIG_SWAP`           | `y`   | Swap (enables zram safety net) |
| `CONFIG_ZRAM`           | `y`   | Compressed RAM swap device |
| `CONFIG_XFS_FS`         | `y`   | XFS filesystem (nested daemon workdir reflink) |
| `CONFIG_KVM`            | `y`   | KVM hypervisor (/dev/kvm passthrough for nested Cloud Hypervisor) |

### PVH boot note

Cloud Hypervisor boots this kernel in **PVH mode** (uncompressed ELF with a PVH entry-point
note). The file is loaded directly — no bootloader, no `bzImage` wrapping. The ELF PVH
note (`type 0x13 = XEN_ELFNOTE_PHYS32_ENTRY`) is verified by `readelf -n`.

## Live boot proof (KV0 equivalent)

Verified 2026-08-12 against Cloud Hypervisor on the build host:

- `TestBootLifecycle` (no initramfs): boot-to-Running in **78 ms**, Pause → Resume → Absent OK
- `TestBootToUserspace` (alpine initramfs): boot-to-userspace in **602 ms**, "Run /init" + "/init reached" markers seen
- In-guest: `ip link add br0 type bridge` → **OK** (serial log: `BRIDGE_TEST: ip link add br0 type bridge -> OK`)
- Kernel message: `Bridge firewalling registered` at `[0.336]` (serial log)
- bridge-nf sysctl: `/proc/sys/net/bridge/bridge-nf-call-iptables = 1`
- IPVS: `IPVS: ipvs loaded.` visible in serial log

## Previous artifact

The prior artifact (6.16.9, committed Aug 7 2026) lacked CONFIG_BRIDGE + CONFIG_BRIDGE_NETFILTER,
blocking docker/compose networking. It is superseded by this from-source 6.12.76 build.
Old SHA-256: `9d3570b47d5abb069ca00edfbfcef4c68306a9c3d078a01f10082b258f1001b8`

## macOS

macOS (Apple Virtualization framework) requires a different kernel compiled with
`virtio_pci` + `virtio_fs` + `vsock` built-in. macOS support is **out of near-term scope**
(ticket 33). A separate `images/kernel/vmlinux-arm64` artifact will be pinned in a future
slice when macOS is targeted.
