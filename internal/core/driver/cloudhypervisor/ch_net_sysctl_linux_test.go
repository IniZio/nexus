//go:build linux

package cloudhypervisor

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestBuildSetForwardingMsg_Encoding(t *testing.T) {
	const ifindex = 42
	msg := buildSetForwardingMsg(ifindex, 0)
	ne := binary.NativeEndian

	if len(msg) != 52 {
		t.Fatalf("len=%d want 52", len(msg))
	}
	if got := ne.Uint32(msg[0:4]); got != 52 {
		t.Errorf("nlmsg_len=%d want 52", got)
	}
	if got := ne.Uint16(msg[4:6]); got != unix.RTM_SETLINK {
		t.Errorf("nlmsg_type=%d want RTM_SETLINK(%d)", got, unix.RTM_SETLINK)
	}
	wantFlags := uint16(unix.NLM_F_REQUEST | unix.NLM_F_ACK)
	if got := ne.Uint16(msg[6:8]); got != wantFlags {
		t.Errorf("nlmsg_flags=0x%x want 0x%x", got, wantFlags)
	}
	if got := int32(ne.Uint32(msg[20:24])); got != int32(ifindex) {
		t.Errorf("ifi_index=%d want %d", got, ifindex)
	}

	checks := []struct {
		off     int
		wantLen uint16
		wantTyp uint16
		label   string
	}{
		{32, 20, unix.IFLA_AF_SPEC | nlaFNested, "IFLA_AF_SPEC"},
		{36, 16, afInetInIFLA | nlaFNested, "AF_INET"},
		{40, 12, unix.IFLA_INET_CONF | nlaFNested, "IFLA_INET_CONF"},
		{44, 8, ipv4DevconfForwarding, "FORWARDING leaf"},
	}
	for _, c := range checks {
		if got := ne.Uint16(msg[c.off : c.off+2]); got != c.wantLen {
			t.Errorf("%s len=%d want %d", c.label, got, c.wantLen)
		}
		if got := ne.Uint16(msg[c.off+2 : c.off+4]); got != c.wantTyp {
			t.Errorf("%s type=0x%x want 0x%x", c.label, got, c.wantTyp)
		}
	}
	if got := ne.Uint32(msg[48:52]); got != 0 {
		t.Errorf("forwarding value=%d want 0", got)
	}
}

const fwdNetnsEnv = "NEXUS_TEST_FWD_NETNS"

func TestSetForwardingNetlink_InNetns(t *testing.T) {
	if os.Getenv(fwdNetnsEnv) == "1" {
		runForwardingNetnsChild(t)
		return
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.Command(exe, "-test.run=TestSetForwardingNetlink_InNetns", "-test.v")
	cmd.Env = append(os.Environ(), fwdNetnsEnv+"=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET,
		UidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getuid(), Size: 1},
		},
		GidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getgid(), Size: 1},
		},
		GidMappingsEnableSetgroups: false,
	}
	out, runErr := cmd.CombinedOutput()
	t.Logf("child:\n%s", out)
	if runErr != nil {
		s := strings.ToLower(string(out) + runErr.Error())
		if strings.Contains(s, "operation not permitted") ||
			strings.Contains(s, "permission denied") ||
			strings.Contains(s, "not supported") {
			t.Skipf("unprivileged user namespaces unavailable: %v", runErr)
		}
		t.Fatalf("child failed: %v", runErr)
	}
}

func runForwardingNetnsChild(t *testing.T) {
	t.Helper()
	const iface = "fwdtest0"
	if out, err := exec.Command("ip", "link", "add", iface, "type", "dummy").CombinedOutput(); err != nil {
		t.Fatalf("ip link add %s type dummy: %v: %s", iface, err, out)
	}

	fwdPath := fmt.Sprintf("/proc/sys/net/ipv4/conf/%s/forwarding", iface)
	if err := os.WriteFile(fwdPath, []byte("1\n"), 0o644); err != nil {
		t.Fatalf("set forwarding=1: %v", err)
	}
	if raw, _ := os.ReadFile(fwdPath); strings.TrimSpace(string(raw)) != "1" {
		t.Fatalf("expected forwarding=1 before test, got %q", raw)
	}

	if err := setIfaceForwardingNetlink(iface); err != nil {
		t.Fatalf("setIfaceForwardingNetlink: %v", err)
	}

	raw, err := os.ReadFile(fwdPath)
	if err != nil {
		t.Fatalf("read forwarding after netlink: %v", err)
	}
	if strings.TrimSpace(string(raw)) != "0" {
		t.Fatalf("forwarding not zero after netlink set, got %q", raw)
	}
}
