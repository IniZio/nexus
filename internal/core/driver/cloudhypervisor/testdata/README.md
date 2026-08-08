# Boot Artifacts for Cloud Hypervisor Integration Tests

These artifacts are used by
`internal/core/driver/cloudhypervisor/boot_integration_test.go`
(build tag `integration`).

**None of the binary artifacts are committed to git** (see `.gitignore`).
Run `scripts/fetch-boot-artifacts.sh` from the repo root to populate them.
The script is idempotent — existing files with correct checksums are not
re-downloaded.

---

## Artifacts

### `vmlinux-x86_64` — primary test kernel

| Field | Value |
|---|---|
| Source | [cloud-hypervisor/linux releases](https://github.com/cloud-hypervisor/linux/releases/tag/ch-release-v6.16.9-20260508) |
| Tag | `ch-release-v6.16.9-20260508` |
| Kernel version | Linux 6.16.9 |
| Format | ELF vmlinux (uncompressed, PVH entry point) |
| SHA-256 | `9d3570b47d5abb069ca00edfbfcef4c68306a9c3d078a01f10082b258f1001b8` |
| Size | ~46 MB |
| Recorded | 2026-08-05 from the above release tag |

**Empirically verified** with Cloud Hypervisor v52.0 on 2026-08-05:
- CH accepts vmlinux as the kernel payload (via `PayloadConfig.kernel`).
- The VM enters "Running" state within ~200 ms of `vm.boot`.
- Without an initramfs the kernel panics and enters an HLT loop; CH
  continues to report `"Running"` indefinitely from that point.
- `vm.pause` returns 204 and CH transitions to `"Paused"`.
- `vm.resume` returns 204 and CH transitions back to `"Running"`.
- CH state strings seen: `"Running"`, `"Paused"` — match `mapCHState`
  exactly; no mismatch observed.
- Boot timing (CH spawn through `vm.boot` 204 response): ~310 ms on
  the test host (12-thread x86, KVM).

### `bzImage-x86_64` — alternate kernel format (also verified)

| Field | Value |
|---|---|
| Source | Same release as vmlinux above |
| Format | bzImage (compressed, 64-bit Linux boot protocol) |
| SHA-256 | `58088758f601a04ef85b09cf23db5530d51edc039ed47afbf2264c5b762cb568` |
| Size | ~8 MB |
| Recorded | 2026-08-05 |

**Empirically verified** with CH v52.0: `vm.create` and `vm.boot` both
return 204, VM reaches `"Running"` within 500 ms. The CH help text reads
"This may be a kernel or firmware that supports a PVH entry point
*(e.g. vmlinux)*" — the parenthetical suggests vmlinux is the primary
format, but bzImage is fully supported in v52.

The integration test uses `vmlinux-x86_64`; `bzImage-x86_64` is present
for format comparisons and as a lighter download for environments where
bandwidth matters.

### `alpine-initramfs.cpio.gz` — initramfs for `TestBootToUserspace`

| Field | Value |
|---|---|
| Upstream source | Alpine Linux 3.20.0 minirootfs |
| Minirootfs URL | `https://dl-cdn.alpinelinux.org/alpine/v3.20/releases/x86_64/alpine-minirootfs-3.20.0-x86_64.tar.gz` |
| Minirootfs SHA-256 | `602efda518516787c716320bd46a3f50e83a74bb749e55483c2f4a9c9f8b9a38` |

Built by `fetch-boot-artifacts.sh` from the Alpine minirootfs with a
custom `/init` injected. The injected `/init`:

1. Mounts devtmpfs on `/dev`
2. Mounts proc on `/proc`
3. Prints `nexus3-test-vm: init reached — sleeping forever` to stdout
4. Loops forever (never calls `poweroff` or `reboot` — that would drive
   the VM to `"Shutdown"`, which the driver maps to `driver.Unknown` and
   would break the pause/resume lifecycle test)

`TestBootToUserspace` passes this initramfs via `Config.InitramfsPath`
with `Cmdline: "console=ttyS0 panic=1"` and `SerialOutputPath` set.
The test asserts the serial log contains the kernel marker
`"Run /init as init process"` and the userspace marker
`"nexus3-test-vm: init reached"`.

**Empirically verified 2026-08-05**: both markers appear within ~600 ms
of the VM entering `Running` state on the test host.

---

## Regenerating Artifacts

```bash
# From the repository root:
bash scripts/fetch-boot-artifacts.sh
```

The script verifies SHA-256 after each download and exits non-zero on
mismatch. To force a re-download, delete the artifact file first.

## Checking for Stale Artifacts

```bash
sha256sum -c - <<'EOF'
9d3570b47d5abb069ca00edfbfcef4c68306a9c3d078a01f10082b258f1001b8  internal/core/driver/cloudhypervisor/testdata/vmlinux-x86_64
58088758f601a04ef85b09cf23db5530d51edc039ed47afbf2264c5b762cb568  internal/core/driver/cloudhypervisor/testdata/bzImage-x86_64
EOF
```
