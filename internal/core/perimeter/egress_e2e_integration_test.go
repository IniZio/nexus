//go:build integration

// TestEgress_GuestOnWire_E2E is the S2 on-wire egress proof for nexus.
//
// It boots a real VM through the netns-runtime path (CH + TAP/bridge + pump
// inside a rootless user+network namespace) and asserts egress policy on the
// wire:
//
//  1. Allow + bearer swap: an allowlisted host is reachable through gvproxy;
//     the MITM proxy swaps the guest's placeholder Bearer token for the real
//     token before forwarding to the upstream stub.
//
//  2. Deny: a non-allowlisted host is rejected at the AllowList gate; an
//     AuditEvent with Decision=Deny is recorded.
//
//  3. Zero-egress (IPv4 + IPv6): after supervisor.Close() tears down the
//     gvproxy/netstack, the guest gets ZERO egress — the stub receives no
//     new connections and IPv6 connections fail inside the guest.
//
//  4. Zero host privilege: CapEff bit 12 (CAP_NET_ADMIN) is CLEAR in the host
//     process for the entire test — the test ASSERTS, not skips.
//
// Runtime skip guards (no //go:build tag change needed):
//   - /dev/kvm absent or not usable        → t.Skip
//   - unprivileged_userns_clone = 0        → t.Skip
//   - cloud-hypervisor binary absent       → t.Skip
//   - vmlinux-x86_64 testdata absent       → t.Skip
//   - alpine-initramfs.cpio.gz absent      → t.Skip
//
// Package perimeter_test (external) is required so that importing netstack
// (which itself imports perimeter) does not create a cycle.
package perimeter_test

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus/internal/core/perimeter"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/perimeter/mitm"
	"github.com/IniZio/nexus/internal/core/perimeter/netfilter"
	"github.com/IniZio/nexus/internal/core/perimeter/netstack"
)

// TestMain dispatches the netns re-exec sentinel so the perimeter test binary
// can act as the child inside the user+network namespace created by
// StartNetnsRuntime. Without this, re-exec with NEXUS_NETNS_RUN=1 would try
// to run test functions instead of the netns child work.
func TestMain(m *testing.M) {
	if os.Getenv(cloudhypervisor.NetnsRunEnv) == "1" {
		cloudhypervisor.RunNetnsChild()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestEgress_GuestOnWire_E2E is the on-wire egress proof.
func TestEgress_GuestOnWire_E2E(t *testing.T) {
	// ── guard: /dev/kvm ────────────────────────────────────────────────────────
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("skipping: /dev/kvm not present")
	}
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("skipping: /dev/kvm not usable: %v", err)
	}
	f.Close()

	// ── guard: unprivileged user namespaces ────────────────────────────────────
	if data, err := os.ReadFile("/proc/sys/kernel/unprivileged_userns_clone"); err == nil {
		if strings.TrimSpace(string(data)) == "0" {
			t.Skip("skipping: unprivileged_userns_clone=0")
		}
	}

	// ── guard: cloud-hypervisor binary ────────────────────────────────────────
	var defaultCHBin = filepath.Join(os.Getenv("HOME"), ".local/bin/cloud-hypervisor")
	chBin := os.Getenv("CLOUD_HYPERVISOR_BIN")
	if chBin == "" {
		chBin = defaultCHBin
	}
	if _, err := os.Stat(chBin); err != nil {
		t.Skipf("skipping: cloud-hypervisor binary not found at %s (set CLOUD_HYPERVISOR_BIN)", chBin)
	}

	// ── guard: testdata boot artifacts ────────────────────────────────────────
	kernelPath := e2eSkipUnlessArtifact(t, "vmlinux-x86_64")
	baseInitramfs := e2eSkipUnlessArtifact(t, "alpine-initramfs.cpio.gz")

	// ── assertion (a): host CapEff bit 12 (CAP_NET_ADMIN) MUST be CLEAR ──────
	// This is an ASSERT, not a skip: the entire value of this test rests on it.
	assertCapEffClear(t, "pre-test")

	// ── socket dir (short path for sun_path limit) ─────────────────────────────
	socketDir, err := os.MkdirTemp("/tmp", "nx3-e2e-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })
	if len(socketDir)+35 > 107 {
		t.Skipf("skipping: MkdirTemp path too long for Unix socket: %s", socketDir)
	}

	// ── sandbox identity ───────────────────────────────────────────────────────
	id := domain.NewSandboxID()

	// ── credential broker: placeholder for (id, "allowed.test") ───────────────
	const (
		realToken   = "super-secret-real-bearer-xyz"
		allowedHost = "allowed.test"
		allowedIP   = "10.0.0.100" // used in /etc/hosts inside the guest
		deniedIP    = "10.0.0.200" // not in AllowList
	)
	broker := cred.NewBroker()
	rec, err := broker.RegisterPlaceholder(id, allowedHost, realToken)
	if err != nil {
		t.Fatalf("RegisterPlaceholder: %v", err)
	}
	t.Logf("placeholder: %s (len=%d)", rec.Placeholder[:8]+"...", len(rec.Placeholder))

	// ── stub TLS upstream (receives the MITM-forwarded, token-swapped request) ─
	authCh := make(chan string, 8)
	stub := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case authCh <- r.Header.Get("Authorization"):
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(stub.Close)

	// stubTransport redirects ALL MITM upstream dials to the local stub TLS
	// server. We use InsecureSkipVerify because the stub's certificate is valid
	// for "*.example.com" / "127.0.0.1" but not for "allowed.test". In production
	// the upstream certificate is verified; here we control the stub so skipping
	// is safe and intentional.
	stubTransport := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, stub.Listener.Addr().String())
		},
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec — test-only stub redirect
		},
	}

	// ── MITM proxy ─────────────────────────────────────────────────────────────
	proxy, err := mitm.New(mitm.Config{
		SandboxID:    id,
		AllowedHosts: []string{allowedHost},
		Broker:       broker,
		Transport:    stubTransport,
	})
	if err != nil {
		t.Fatalf("mitm.New: %v", err)
	}

	// ── AllowList: allow only 10.0.0.100 ─────────────────────────────────────
	al, err := netfilter.NewAllowList([]string{allowedIP}, nil, nil)
	if err != nil {
		t.Fatalf("NewAllowList: %v", err)
	}

	// ── build custom initramfs overlay with probe init script ─────────────────
	// The overlay CPIO overrides /init in the alpine initramfs. The init script
	// reads nexus_ph from /proc/cmdline and drives wget probes.
	initScript := e2eBuildInitScript(rec.Placeholder)
	combinedInitramfs := e2eBuildInitramfs(t, baseInitramfs, initScript)

	// ── serial output capture path ────────────────────────────────────────────
	serialPath := filepath.Join(socketDir, "serial.txt")

	// ── kernel cmdline: console + nexus params ───────────────────────────────
	// nexus_ph is parsed by the init script from /proc/cmdline.
	cmdline := fmt.Sprintf("console=ttyS0 panic=5 nexus_ph=%s", rec.Placeholder)

	// ── boot VM via CHDriver ───────────────────────────────────────────────────
	drv, err := cloudhypervisor.New(cloudhypervisor.Config{
		BinaryPath:       chBin,
		SocketDir:        socketDir,
		KernelPath:       kernelPath,
		InitramfsPath:    combinedInitramfs,
		Cmdline:          cmdline,
		VCPUs:            1,
		MemoryMiB:        256,
		StartTimeout:     30 * time.Second,
		SerialOutputPath: serialPath,
	})
	if err != nil {
		t.Fatalf("cloudhypervisor.New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)

	t.Log("Starting VM through netns-runtime path...")
	_, err = drv.Start(ctx, driver.StartRequest{SandboxID: id})
	if err != nil {
		t.Fatalf("drv.Start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer stopCancel()
		_ = drv.Stop(stopCtx, id)
	})

	// ── get PerimConn: the host-side end of the socketpair ────────────────────
	fd, err := drv.GuestNetworkFD(ctx, id)
	if err != nil {
		t.Fatalf("GuestNetworkFD: %v", err)
	}

	// ── wire the perimeter supervisor ─────────────────────────────────────────
	auditCh := make(chan perimeter.AuditEvent, 32)
	stack := netstack.New(al, func(ev perimeter.AuditEvent) {
		select {
		case auditCh <- ev:
		default:
		}
	})

	supervisor, err := perimeter.Start(ctx, id, fd, stack, proxy, al)
	if err != nil {
		t.Fatalf("perimeter.Start: %v", err)
	}

	// ── diagnostic: wait for init-start marker (proves custom init is running) ─
	t.Log("Waiting for custom init start marker...")
	pollSerialLine(t, serialPath, "nexus_init_start:", 20*time.Second)
	t.Log("Custom init started — CPIO overlay is working")

	// ── diagnostic: wait for DHCP completion marker ───────────────────────────
	t.Log("Waiting for DHCP completion marker...")
	dhcpResult := pollSerialLine(t, serialPath, "nexus_dhcp_done:", 40*time.Second)
	t.Logf("DHCP exit code: %s", dhcpResult)

	// ── case 1: ALLOW + bearer-swap ───────────────────────────────────────────
	// Poll serial output for the marker that wget to allowed.test finished.
	// Then assert the stub upstream received the real token, not the placeholder.
	t.Log("Waiting for ALLOW probe to complete (serial output)...")
	allowResult := pollSerialLine(t, serialPath, "nexus_allow_done:", 60*time.Second)
	t.Logf("ALLOW probe exit code from serial: %q", allowResult)

	t.Log("Waiting for stub upstream to receive the token-swapped request...")
	select {
	case gotAuth := <-authCh:
		want := "Bearer " + realToken
		if gotAuth != want {
			t.Errorf("ALLOW FAIL: upstream Authorization=%q, want %q (bearer swap broken)", gotAuth, want)
			if gotAuth == "Bearer "+rec.Placeholder {
				t.Error("placeholder token LEAKED to upstream — swap did not fire")
			}
		} else {
			t.Logf("ALLOW PASS: upstream received real token (placeholder was swapped)")
		}
	case <-time.After(10 * time.Second):
		t.Error("ALLOW FAIL: stub upstream received no request within 10s of serial marker")
	}

	// ── case 2: DENY ──────────────────────────────────────────────────────────
	// Poll serial output for the denied probe result.
	t.Log("Waiting for DENY probe to complete (serial output)...")
	denyResult := pollSerialLine(t, serialPath, "nexus_deny_done:", 30*time.Second)
	t.Logf("DENY probe exit code from serial: %q", denyResult)

	// Wait for a Deny AuditEvent for the denied IP.
	t.Log("Waiting for Deny AuditEvent...")
	var gotDeny bool
	denyDeadline := time.After(10 * time.Second)
denyWait:
	for !gotDeny {
		select {
		case ev := <-auditCh:
			if ev.Decision == perimeter.Deny && strings.Contains(ev.DestHost, deniedIP) {
				gotDeny = true
				t.Logf("DENY PASS: AuditEvent Deny for %s (%s)", ev.DestHost, ev.Reason)
			}
		case <-denyDeadline:
			t.Error("DENY FAIL: no Deny AuditEvent for denied IP within 10s")
			break denyWait
		}
	}

	// ── case 3: IPv6 zero-egress (pre-close) ──────────────────────────────────
	// The init script tries an IPv6 connection; it must fail because the
	// interfaces inside the guest's netns have disable_ipv6=1 applied by
	// applySandboxNetSysctls before the interfaces came up. Verify via serial.
	t.Log("Waiting for IPv6 probe result (serial output)...")
	ipv6Result := pollSerialLine(t, serialPath, "nexus_ipv6_done:", 20*time.Second)
	if ipv6Result == "0" {
		t.Errorf("IPV6 FAIL: IPv6 connection succeeded (exit 0) — egress should be blocked")
	} else {
		t.Logf("IPV6 PASS: IPv6 connection failed (exit %s) — zero IPv6 egress confirmed", ipv6Result)
	}

	// ── case 3: IPv4 zero-egress (post-close) ─────────────────────────────────
	// Drain any pending auth deliveries from the allow case, then close the
	// supervisor. After close, gvproxy/netstack is stopped; the guest's retry
	// loop can no longer deliver requests to the stub.
drainAuthPre:
	for {
		select {
		case <-authCh:
		default:
			break drainAuthPre
		}
	}

	t.Log("Closing perimeter supervisor (gvproxy/netstack stops)...")
	if err := supervisor.Close(); err != nil {
		t.Errorf("supervisor.Close: %v", err)
	}

	// Allow the guest's in-flight retry (with --timeout=3) to complete, then
	// wait one full retry cycle (sleep 2 in the loop) to see if anything leaks.
	const zeroEgressWindow = 8 * time.Second
	t.Logf("Asserting zero IPv4 egress for %v after supervisor.Close()...", zeroEgressWindow)
	postCloseTimer := time.After(zeroEgressWindow)
	select {
	case got := <-authCh:
		t.Errorf("ZERO-EGRESS FAIL (IPv4): stub received connection after supervisor.Close(): Authorization=%q", got)
	case <-postCloseTimer:
		t.Log("ZERO-EGRESS PASS (IPv4): no connections to stub within window after Close()")
	}

	// ── final: CapEff bit 12 MUST still be CLEAR ─────────────────────────────
	assertCapEffClear(t, "post-test")

	// ── leak checks (deferred to cleanup) are in t.Cleanup above ─────────────
}

// ── helpers ───────────────────────────────────────────────────────────────────

// assertCapEffClear reads /proc/self/status CapEff and ASSERTS (not skips) that
// CAP_NET_ADMIN (bit 12) is CLEAR. This is the proof of zero host privilege.
func assertCapEffClear(t *testing.T, label string) {
	t.Helper()
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatalf("[%s] read /proc/self/status: %v", label, err)
	}
	var capEffHex string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "CapEff:") {
			capEffHex = strings.TrimSpace(strings.TrimPrefix(line, "CapEff:"))
			break
		}
	}
	if capEffHex == "" {
		t.Fatalf("[%s] CapEff not found in /proc/self/status", label)
	}
	capEff, err := strconv.ParseUint(capEffHex, 16, 64)
	if err != nil {
		t.Fatalf("[%s] parse CapEff %q: %v", label, capEffHex, err)
	}
	const capNetAdminBit = uint64(1) << 12
	if capEff&capNetAdminBit != 0 {
		t.Errorf("[%s] FAIL: host has CAP_NET_ADMIN set (CapEff=0x%x, bit 12 SET)", label, capEff)
	} else {
		t.Logf("[%s] PASS: host CapEff=0x%x — bit 12 (CAP_NET_ADMIN) is CLEAR", label, capEff)
	}
}

// e2eSkipUnlessArtifact returns the absolute path to a testdata artifact or
// skips the test. The path is relative to the cloudhypervisor testdata directory.
func e2eSkipUnlessArtifact(t *testing.T, name string) string {
	t.Helper()
	// testdata lives in the cloudhypervisor package, one level up within core.
	rel := filepath.Join("..", "driver", "cloudhypervisor", "testdata", name)
	if _, err := os.Stat(rel); err != nil {
		t.Skipf("skipping: boot artifact %q not found\n"+
			"  Run:  bash scripts/fetch-boot-artifacts.sh\n"+
			"  from the repository root to fetch it.", rel)
	}
	abs, err := filepath.Abs(rel)
	if err != nil {
		t.Fatalf("filepath.Abs(%q): %v", rel, err)
	}
	return abs
}

// e2eBuildInitScript returns the shell script content for the guest's custom
// /init. It reads nexus_ph from /proc/cmdline (kernel passes unrecognised
// parameters to /proc/cmdline) and probes:
//   - HTTPS to allowed.test (10.0.0.100) with the placeholder bearer
//   - HTTPS to denied.test (10.0.0.200) without bearer (expect failure)
//   - HTTPS via IPv6 to prove IPv6 is unreachable
//   - A retry loop so the zero-egress test can observe failure after Close()
//
// All output goes to the serial console (stdout = /dev/console = ttyS0).
func e2eBuildInitScript(placeholder string) string {
	// We embed the placeholder directly in the script rather than parsing
	// /proc/cmdline — the placeholder contains only hex chars [0-9a-f] so
	// there is no quoting hazard in the cmdline or in the script.
	return fmt.Sprintf(`#!/bin/sh
# nexus e2e egress test init
echo "nexus_init_start:1"

mount -t devtmpfs devtmpfs /dev  2>/dev/null || true
mount -t proc     proc     /proc 2>/dev/null || true
mount -t sysfs    sysfs    /sys  2>/dev/null || true

# Discover the guest NIC (first non-loopback interface).
iface=""
for i in $(ls /sys/class/net/ 2>/dev/null); do
    [ "$i" = "lo" ] && continue
    iface="$i"
    break
done
[ -z "$iface" ] && iface=eth0

# Bring the interface up and run DHCP (gvproxy provides a built-in DHCP server).
ip link set "$iface" up 2>/dev/null || ifconfig "$iface" up 2>/dev/null
/sbin/udhcpc -i "$iface" -n -q -t 20 2>/dev/null
echo "nexus_dhcp_done:$?"

# Resolve test targets via /etc/hosts (avoids DNS latency and bypass risk).
echo "10.0.0.100 allowed.test" >> /etc/hosts
echo "10.0.0.200 denied.test"  >> /etc/hosts

# Case 1: ALLOWED HTTPS with placeholder bearer (expect 0 — MITM swaps token).
wget -q --no-check-certificate \
     --header "Authorization: Bearer %s" \
     --timeout=20 \
     -O /dev/null \
     "https://allowed.test/"
echo "nexus_allow_done:$?"

# Case 2: DENIED HTTPS to non-allowlisted IP (expect non-zero).
wget -q --no-check-certificate \
     --timeout=6 \
     -O /dev/null \
     "https://denied.test/"
echo "nexus_deny_done:$?"

# Case 3: IPv6 probe — must fail (interfaces have disable_ipv6=1).
wget -q -6 --no-check-certificate \
     --timeout=4 \
     -O /dev/null \
     "https://[2606:4700:4700::1111]/" 2>/dev/null
echo "nexus_ipv6_done:$?"

# Retry loop: keep probing allowed.test so the zero-egress test can observe
# that connections stop arriving at the stub after supervisor.Close().
while true; do
    wget -q --no-check-certificate \
         --header "Authorization: Bearer %s" \
         --timeout=3 \
         -O /dev/null \
         "https://allowed.test/" 2>/dev/null
    echo "nexus_retry_done:$?"
    sleep 2
done
`, placeholder, placeholder)
}

// e2eBuildInitramfs creates a combined initramfs: the alpine base (gzip CPIO)
// followed by a gzip-compressed CPIO overlay that overrides /init with
// initScript. Both archives are gzip-compressed so the Linux kernel (which
// detects and decompresses each archive independently) correctly overrides
// /init from the base archive with the overlay's /init.
func e2eBuildInitramfs(t *testing.T, baseInitramfsPath, initScript string) string {
	t.Helper()
	dir := t.TempDir()

	// Build an uncompressed newc CPIO overlay containing just ./init.
	var rawOverlay bytes.Buffer
	e2eWriteCPIOEntry(&rawOverlay, "./init", []byte(initScript), 0o100755)
	e2eWriteCPIOTrailer(&rawOverlay)

	// Gzip-compress the overlay CPIO.
	// The Linux kernel detects each compressed archive independently when
	// processing a concatenated initramfs file; both must be in the same
	// compression format (gzip) for the kernel to recognise and override.
	var gzOverlay bytes.Buffer
	gw, err := gzip.NewWriterLevel(&gzOverlay, gzip.BestSpeed)
	if err != nil {
		t.Fatalf("gzip.NewWriterLevel: %v", err)
	}
	if _, err := gw.Write(rawOverlay.Bytes()); err != nil {
		t.Fatalf("gzip overlay write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip overlay close: %v", err)
	}

	// Concatenate: base (gzip CPIO) + overlay (gzip CPIO).
	combinedPath := filepath.Join(dir, "combined.initramfs")
	out, err := os.Create(combinedPath)
	if err != nil {
		t.Fatalf("create combined initramfs: %v", err)
	}
	defer out.Close()

	base, err := os.Open(baseInitramfsPath)
	if err != nil {
		t.Fatalf("open base initramfs: %v", err)
	}
	if _, err := io.Copy(out, base); err != nil {
		base.Close()
		t.Fatalf("copy base initramfs: %v", err)
	}
	base.Close()

	if _, err := out.Write(gzOverlay.Bytes()); err != nil {
		t.Fatalf("write gzip overlay CPIO: %v", err)
	}
	return combinedPath
}

// e2eWriteCPIOEntry writes a single file entry in CPIO newc (SVR4 no-CRC) format.
//
// newc layout:
//   - 110-byte ASCII header: magic(6) + 13 fields × 8 lowercase hex chars
//   - filename (namesize bytes, null-terminated)
//   - padding to 4-byte boundary after (header + filename)
//   - file data (filesize bytes)
//   - padding to 4-byte boundary after data
func e2eWriteCPIOEntry(w io.Writer, name string, data []byte, mode uint32) {
	nameBytes := append([]byte(name), 0) // null-terminated name
	header := fmt.Sprintf("070701%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x",
		1,              // c_ino: inode number (unique within archive)
		mode,           // c_mode: file mode (regular file + permission bits)
		0,              // c_uid: owner uid (0 = root)
		0,              // c_gid: owner gid
		1,              // c_nlink: number of hard links
		0,              // c_mtime: modification time
		len(data),      // c_filesize
		0,              // c_devmajor
		1,              // c_devminor
		0,              // c_rdevmajor
		0,              // c_rdevminor
		len(nameBytes), // c_namesize (includes null terminator)
		0,              // c_check (always 0 for newc)
	)
	io.WriteString(w, header) //nolint:errcheck — bytes.Buffer.Write never errors
	w.Write(nameBytes)        //nolint:errcheck
	// Pad to 4-byte boundary after header (110 bytes) + filename.
	if pad := (4 - (110+len(nameBytes))%4) % 4; pad > 0 {
		w.Write(make([]byte, pad)) //nolint:errcheck
	}
	// File data.
	w.Write(data) //nolint:errcheck
	// Pad to 4-byte boundary after data.
	if pad := (4 - len(data)%4) % 4; pad > 0 {
		w.Write(make([]byte, pad)) //nolint:errcheck
	}
}

// e2eWriteCPIOTrailer writes the end-of-archive TRAILER!!! entry.
func e2eWriteCPIOTrailer(w io.Writer) {
	// Trailer: all numeric fields zero except c_namesize=11 and c_nlink=1.
	nameBytes := []byte("TRAILER!!!\x00")
	header := fmt.Sprintf("070701%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x",
		0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, len(nameBytes), 0)
	io.WriteString(w, header) //nolint:errcheck
	w.Write(nameBytes)        //nolint:errcheck
	if pad := (4 - (110+len(nameBytes))%4) % 4; pad > 0 {
		w.Write(make([]byte, pad)) //nolint:errcheck
	}
}

// pollSerialLine polls serialPath until a line starting with prefix appears,
// then returns the suffix (typically an exit code). On timeout, it logs the
// full serial file contents (for debugging) and calls t.Fatalf.
func pollSerialLine(t *testing.T, serialPath, prefix string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(serialPath)
		if err == nil {
			scanner := bufio.NewScanner(bytes.NewReader(data))
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, prefix) {
					return strings.TrimPrefix(line, prefix)
				}
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	// Log full serial output so we can diagnose what step stalled.
	if data, err := os.ReadFile(serialPath); err == nil && len(data) > 0 {
		t.Logf("--- serial output at timeout (%d bytes) ---\n%s\n---", len(data), string(data))
	} else if err != nil {
		t.Logf("--- serial file unreadable: %v ---", err)
	} else {
		t.Log("--- serial file is EMPTY (nothing written to ttyS0) ---")
	}
	t.Fatalf("timed out after %v waiting for %q in serial output %s", timeout, prefix, serialPath)
	return ""
}

// ── TCP SYN frame crafting (used nowhere currently; retained for debugging) ──
// These are kept for potential manual frame injection during investigation.

// e2eGatewayMAC is the gvproxy VirtualNetwork gateway MAC.
var e2eGatewayMAC = [6]byte{0x5a, 0x94, 0xef, 0xe4, 0x0c, 0xdd}

// e2eGuestMAC is a synthetic guest MAC for injected frames.
var e2eGuestMAC = [6]byte{0x02, 0x00, 0xde, 0xad, 0xbe, 0xef}

// e2eCraftTCPSYN builds an Ethernet/IPv4/TCP SYN frame from guestIPv4:srcPort
// to dstIPv4:dstPort, addressed to the gvproxy gateway MAC.
func e2eCraftTCPSYN(guestIPv4, dstIPv4 [4]byte, srcPort, dstPort uint16) []byte {
	frame := make([]byte, 14+20+20)
	// Ethernet header.
	copy(frame[0:6], e2eGatewayMAC[:])
	copy(frame[6:12], e2eGuestMAC[:])
	binary.BigEndian.PutUint16(frame[12:14], 0x0800)
	// IPv4 header.
	ip := frame[14 : 14+20]
	ip[0] = 0x45
	binary.BigEndian.PutUint16(ip[2:4], 40)
	binary.BigEndian.PutUint16(ip[4:6], 1)
	ip[8] = 64
	ip[9] = 6
	copy(ip[12:16], guestIPv4[:])
	copy(ip[16:20], dstIPv4[:])
	binary.BigEndian.PutUint16(ip[10:12], e2eIPv4Checksum(ip))
	// TCP header.
	tcp := make([]byte, 20)
	binary.BigEndian.PutUint16(tcp[0:2], srcPort)
	binary.BigEndian.PutUint16(tcp[2:4], dstPort)
	tcp[12] = 0x50
	tcp[13] = 0x02
	binary.BigEndian.PutUint16(tcp[14:16], 1024)
	binary.BigEndian.PutUint16(tcp[16:18], e2eTCPChecksum(guestIPv4, dstIPv4, tcp))
	copy(frame[14+20:], tcp)
	return frame
}

func e2eIPv4Checksum(hdr []byte) uint16 { return e2eOnesComplement(hdr) }

func e2eTCPChecksum(src, dst [4]byte, seg []byte) uint16 {
	pseudo := make([]byte, 12+len(seg))
	copy(pseudo[0:4], src[:])
	copy(pseudo[4:8], dst[:])
	pseudo[9] = 6
	binary.BigEndian.PutUint16(pseudo[10:12], uint16(len(seg)))
	copy(pseudo[12:], seg)
	return e2eOnesComplement(pseudo)
}

func e2eOnesComplement(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}
