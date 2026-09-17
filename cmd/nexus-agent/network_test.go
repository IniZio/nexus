package main

// network_test.go — regression tests for firstNonLoIfaceAt.
//
// Root cause: CONFIG_DUMMY=y in the 6.12.76 guest kernel creates dummy0 at
// boot. Because "dummy" < "eth" alphabetically, the original firstNonLoIface
// picked dummy0 before the virtio-net interface (eth0), assigning the guest IP
// to a black-hole device and breaking egress. The fix uses /sys/class/net
// device symlinks to distinguish hardware-backed from virtual interfaces.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// makeFakeSysfs creates a minimal /sys/class/net tree under root:
//
//	root/class/net/lo/           (no device — loopback)
//	root/class/net/dummy0/       (no device — virtual, CONFIG_DUMMY=y)
//	root/class/net/eth0/         (has device — virtio-net)
//	  root/class/net/eth0/device → <symlink target>
//
// The presence or absence of the "device" entry determines whether an
// interface is hardware-backed. An empty file works as a stand-in for the
// symlink target; os.Stat follows symlinks so we use a regular file.
func makeFakeSysfs(t *testing.T, ifaces map[string]bool) string {
	t.Helper()
	root := t.TempDir()
	netDir := filepath.Join(root, "class", "net")
	for iface, hasDevice := range ifaces {
		dir := filepath.Join(netDir, iface)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if hasDevice {
			// Represent the "device" symlink with a regular file so that
			// os.Stat succeeds without needing a real PCI sysfs tree.
			devFile := filepath.Join(dir, "device")
			if err := os.WriteFile(devFile, nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

// TestFirstNonLoIfaceAt_DummySkipped is the regression test for the
// CONFIG_DUMMY=y bug. Before the fix, firstNonLoIfaceAt returned "dummy0"
// when dummy0 existed alongside eth0 (alphabetical ordering). After the fix
// it must return "eth0" (hardware-backed) and skip "dummy0" (no device link).
func TestFirstNonLoIfaceAt_DummySkipped(t *testing.T) {
	root := makeFakeSysfs(t, map[string]bool{
		"lo":     false, // loopback — always skipped
		"dummy0": false, // virtual (CONFIG_DUMMY=y) — no device link
		"eth0":   true,  // virtio-net — has device link
	})

	got := firstNonLoIfaceAt(root)
	if got != "eth0" {
		t.Errorf("firstNonLoIfaceAt: got %q, want %q (dummy0 must be skipped)", got, "eth0")
	}
}

// TestFirstNonLoIfaceAt_NoDeviceLinks tests the fallback path for kernels
// where all interfaces lack device symlinks (e.g. old test-fixture kernels
// without CONFIG_DUMMY=y that only expose lo and eth0 without device entries).
// The fallback must return the first non-lo interface alphabetically.
func TestFirstNonLoIfaceAt_NoDeviceLinks(t *testing.T) {
	root := makeFakeSysfs(t, map[string]bool{
		"lo":   false,
		"eth0": false, // no device link — fallback path
	})

	got := firstNonLoIfaceAt(root)
	if got != "eth0" {
		t.Errorf("firstNonLoIfaceAt: got %q, want %q", got, "eth0")
	}
}

// TestFirstNonLoIfaceAt_OnlyLoopback verifies that "" is returned when the
// only interface is lo (extreme case: no network device at all).
func TestFirstNonLoIfaceAt_OnlyLoopback(t *testing.T) {
	root := makeFakeSysfs(t, map[string]bool{
		"lo": false,
	})

	got := firstNonLoIfaceAt(root)
	if got != "" {
		t.Errorf("firstNonLoIfaceAt: got %q, want empty string", got)
	}
}

// TestFirstNonLoIfaceAt_MultipleDeviceBacked verifies that the first
// alphabetical hardware-backed interface is returned when multiple exist.
func TestFirstNonLoIfaceAt_MultipleDeviceBacked(t *testing.T) {
	root := makeFakeSysfs(t, map[string]bool{
		"lo":     false,
		"dummy0": false,
		"eth0":   true,
		"eth1":   true,
	})

	got := firstNonLoIfaceAt(root)
	if got != "eth0" {
		t.Errorf("firstNonLoIfaceAt: got %q, want %q", got, "eth0")
	}
}

// checkEgressWith tests

// devnull opens /dev/null as a *os.File for use as a silent console sink in
// tests that don't need to inspect log output.
func devnull(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// okProbe is a fake DNS probe that always reports success.
func okProbe(addr string, timeout time.Duration) error { return nil }

// failProbe is a fake DNS probe that always reports a timeout.
func failProbe(addr string, timeout time.Duration) error {
	return fmt.Errorf("i/o timeout")
}

// TestCheckEgressWith_HardwareBacked_DNSOk verifies that a hardware-backed
// interface with a reachable DNS server reports success (returns true, logs
// the "egress self-check ok" line).
func TestCheckEgressWith_HardwareBacked_DNSOk(t *testing.T) {
	root := makeFakeSysfs(t, map[string]bool{
		"lo":   false,
		"eth0": true, // hardware-backed
	})

	ok := checkEgressWith("eth0", root, okProbe, devnull(t))
	if !ok {
		t.Error("checkEgressWith: want ok=true for hw-backed iface + live DNS, got false")
	}
}

// TestCheckEgressWith_NonHWBacked_Fails is the regression test for the dummy0
// class of bug: a virtual interface (no sysfs device link) must be detected
// and the check must report failure even when DNS appears reachable.
func TestCheckEgressWith_NonHWBacked_Fails(t *testing.T) {
	root := makeFakeSysfs(t, map[string]bool{
		"lo":     false,
		"dummy0": false, // no device link — virtual interface
	})

	ok := checkEgressWith("dummy0", root, okProbe, devnull(t))
	if ok {
		t.Error("checkEgressWith: want ok=false for non-hw-backed iface (dummy0), got true")
	}
}

// TestCheckEgressWith_DNSFails verifies that a hardware-backed interface
// reports failure when the DNS/gateway probe times out.
func TestCheckEgressWith_DNSFails(t *testing.T) {
	root := makeFakeSysfs(t, map[string]bool{
		"lo":   false,
		"eth0": true,
	})

	ok := checkEgressWith("eth0", root, failProbe, devnull(t))
	if ok {
		t.Error("checkEgressWith: want ok=false when DNS probe fails, got true")
	}
}

// TestCheckEgressWith_FailureDiagnostic verifies that the greppable failure
// marker "EGRESS SELF-CHECK FAILED" appears in the console log and includes
// the specific failure details (hwbacked, gateway, dns fields).
func TestCheckEgressWith_FailureDiagnostic(t *testing.T) {
	root := makeFakeSysfs(t, map[string]bool{
		"dummy0": false, // non-hw-backed → triggers failure
	})

	// Write console output to a temp file so we can inspect it.
	f, err := os.CreateTemp(t.TempDir(), "console-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	checkEgressWith("dummy0", root, okProbe, f)

	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4096)
	n, _ := f.Read(buf)
	out := string(buf[:n])

	for _, want := range []string{
		"EGRESS SELF-CHECK FAILED",
		"iface=dummy0",
		"hwbacked=false",
		"gateway=" + guestNetworkGateway,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("failure log missing %q; got: %q", want, out)
		}
	}
}
