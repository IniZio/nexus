//go:build integration && linux

package cloudhypervisor

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/hostbin"
	"github.com/IniZio/nexus/internal/core/perimeter/netfilter"
	"github.com/IniZio/nexus/internal/core/perimeter/netstack"
)

const vhostProbeInit = `#!/bin/sh
mount -t devtmpfs devtmpfs /dev 2>/dev/null
mount -t proc proc /proc 2>/dev/null
mount -t sysfs sysfs /sys 2>/dev/null
exec >/dev/console 2>&1
ip link set lo up
IFACE=""
for d in /sys/class/net/*; do
    n=$(basename "$d")
    [ "$n" = "lo" ] && continue
    [ -e "$d/device" ] || continue
    IFACE="$n"
    break
done
echo "S9B3:IFACE:$IFACE"
ip link set "$IFACE" up
udhcpc -i "$IFACE" -n -q -t 5 -T 2
echo "S9B3:IP:$(ip -4 -o addr show "$IFACE" | tr -s ' ' | cut -d' ' -f4)"
ping -c 2 -W 3 192.168.127.1 >/dev/null 2>&1 && echo "S9B3:PING:OK" || echo "S9B3:PING:FAIL"
echo "S9B3:FETCH_IP:$(wget -q -T 10 -O - http://10.9.9.9/ 2>&1 | head -c 64)"
nslookup @NAME@ 192.168.127.1 >/dev/null 2>&1 && echo "S9B3:DNS:OK" || echo "S9B3:DNS:FAIL"
echo "S9B3:FETCH_NAME:$(wget -q -T 15 -O - http://@NAME@/ 2>&1 | head -c 64)"
echo "S9B3:DONE"
while true; do sleep 60; done
`

func cpioNewc(files map[string]string) []byte {
	var b bytes.Buffer
	pad := func() {
		for b.Len()%4 != 0 {
			b.WriteByte(0)
		}
	}
	ino := 1
	add := func(name string, mode int, data string) {
		fmt.Fprintf(&b, "070701%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x",
			ino, mode, 0, 0, 1, 0, len(data), 0, 0, 0, 0, len(name)+1, 0)
		ino++
		b.WriteString(name)
		b.WriteByte(0)
		pad()
		b.WriteString(data)
		pad()
	}
	for n, d := range files {
		add(n, 0o100755, d)
	}
	add("TRAILER!!!", 0, "")
	return b.Bytes()
}

func appendInit(t *testing.T, base string, script string) string {
	t.Helper()
	raw, err := os.ReadFile(base)
	if err != nil {
		t.Fatal(err)
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(cpioNewc(map[string]string{"init": script}))
	_ = zw.Close()
	out := filepath.Join(t.TempDir(), "probe-initramfs.cpio.gz")
	if err := os.WriteFile(out, append(raw, gz.Bytes()...), 0o600); err != nil {
		t.Fatal(err)
	}
	return out
}

func vhostCHBin(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("CLOUD_HYPERVISOR_BIN"); p != "" {
		return p
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, err := hostbin.Resolve(ctx, hostbin.CloudHypervisor)
	if err != nil {
		t.Skipf("cloud-hypervisor unavailable (set CLOUD_HYPERVISOR_BIN): %v", err)
	}
	return p
}

func openFDCount(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(ents)
}

// TestVhostNet_GuestNetworking boots a real CH against the vhost-user backend
// in an empty netns with no `ip` on PATH and drives DHCP, ICMP, DNS and HTTP
// from the guest through the userspace netstack.
func TestVhostNet_GuestNetworking(t *testing.T) {
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("/dev/kvm not usable: %v", err)
	}
	f.Close()
	chBin := vhostCHBin(t)
	probeName := "example.com"
	_, lookupErr := net.LookupHost(probeName)

	kernel := netnsSkipUnlessArtifact(t, "vmlinux-x86_64")
	initramfs := appendInit(t, netnsSkipUnlessArtifact(t, "alpine-initramfs.cpio.gz"), strings.ReplaceAll(vhostProbeInit, "@NAME@", probeName))

	t.Setenv("PATH", t.TempDir())

	dir, err := os.MkdirTemp("/var/tmp", "vhn-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	serial := filepath.Join(dir, "serial.log")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "vhost-ok:"+r.Host)
	}))
	defer srv.Close()

	al, err := netfilter.NewAllowList([]string{"10.9.9.9"}, nil, allowDomains(lookupErr == nil, probeName))
	if err != nil {
		t.Fatal(err)
	}
	al.Start(30 * time.Second)
	defer al.Stop()
	stack := netstack.New(al, nil, netstack.WithDialer(func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", srv.Listener.Addr().String())
	}))

	fdsBefore := openFDCount(t)
	id := domain.NewSandboxID()
	cfg := Config{BinaryPath: chBin, SocketDir: dir, StartTimeout: 20 * time.Second, NetMode: domain.NetModeVhostUser}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	sock := filepath.Join(dir, "ch.sock")
	rt, err := StartNetnsRuntime(ctx, cfg, id, sock, "")
	if err != nil {
		t.Fatalf("StartNetnsRuntime: %v", err)
	}
	t.Cleanup(rt.Stop)
	if rt.GuestTap != "" || rt.VhostSocket == "" {
		t.Fatalf("rt tap=%q vhost=%q", rt.GuestTap, rt.VhostSocket)
	}

	go func() { _ = stack.Run(ctx, id, rt.PerimConn) }()

	c := newClient(sock)
	deadline := time.Now().Add(25 * time.Second)
	for {
		pctx, pc := context.WithTimeout(ctx, 500*time.Millisecond)
		err = c.Ping(pctx)
		pc()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("CH API not ready: %v\nchild stderr:\n%s", err, rt.ChildStderr())
		}
		time.Sleep(50 * time.Millisecond)
	}

	if st, err := os.Stat(rt.VhostSocket); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("vhost socket stat: %v %v", st, err)
	}

	vmcfg := vmConfig{
		Payload: vmPayloadConfig{Kernel: kernel, Initramfs: initramfs, Cmdline: "console=ttyS0 panic=5"},
		CPUs:    &vmCPUsConfig{BootVCPUs: 1, MaxVCPUs: 1},
		Memory:  buildMemoryConfig(cfg, 256, true),
		Serial:  &vmSerialConfig{Mode: "File", File: serial},
	}
	if !vmcfg.Memory.Shared {
		t.Fatal("memory not shared")
	}
	if err := c.VMCreateWithNet(ctx, vmcfg, nil, buildNets(cfg, rt.GuestTap, rt.VhostSocket, id), nil); err != nil {
		t.Fatalf("vm.create: %v\nchild stderr:\n%s", err, rt.ChildStderr())
	}
	if err := c.VMBoot(ctx); err != nil {
		t.Fatalf("vm.boot: %v\nchild stderr:\n%s", err, rt.ChildStderr())
	}

	var out string
	for end := time.Now().Add(90 * time.Second); time.Now().Before(end); time.Sleep(500 * time.Millisecond) {
		b, _ := os.ReadFile(serial)
		out = string(b)
		if strings.Contains(out, "S9B3:DONE") {
			break
		}
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "S9B3:") {
			t.Log(strings.TrimSpace(l))
		}
	}
	for _, want := range []string{"S9B3:IP:192.168.127.2/", "S9B3:PING:OK", "S9B3:FETCH_IP:vhost-ok:"} {
		if !strings.Contains(out, want) {
			t.Errorf("serial missing %q\n%s\nchild stderr:\n%s", want, tail(out, 3000), rt.ChildStderr())
		}
	}
	if lookupErr == nil {
		for _, want := range []string{"S9B3:DNS:OK", "S9B3:FETCH_NAME:vhost-ok:"} {
			if !strings.Contains(out, want) {
				t.Errorf("serial missing %q\n%s", want, tail(out, 3000))
			}
		}
	} else {
		t.Logf("host cannot resolve %s (%v); DNS/named fetch not asserted", probeName, lookupErr)
	}

	hostNS, _ := os.Readlink("/proc/self/ns/net")
	childNS, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/net", rt.ChildPID))
	if err != nil || childNS == hostNS {
		t.Errorf("child netns %q vs host %q (%v)", childNS, hostNS, err)
	}
	chPID := 0
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		var pid int
		if _, serr := fmt.Sscanf(e.Name(), "%d", &pid); serr != nil || pid == rt.ChildPID {
			continue
		}
		if st, serr := ReadProcStat(pid); serr == nil && st.PGID == rt.ChildPGID {
			chPID = pid
		}
	}
	if chPID == 0 {
		t.Fatal("CH process not found in child group")
	}
	if chNS, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/net", chPID)); err != nil || chNS == hostNS {
		t.Errorf("CH netns %q vs host %q (%v)", chNS, hostNS, err)
	}

	rt.Stop()
	if !waitForGroupExit(rt.ChildPGID, 5*time.Second) {
		t.Errorf("process group %d still alive after Stop", rt.ChildPGID)
	}
	for _, p := range []string{rt.VhostSocket, rt.ControlSocket, rt.ControlToken} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s not removed (%v)", p, err)
		}
	}
	cancel()
	time.Sleep(500 * time.Millisecond)
	if after := openFDCount(t); after > fdsBefore+2 {
		t.Errorf("fd leak: before=%d after=%d", fdsBefore, after)
	}
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func allowDomains(ok bool, name string) []string {
	if !ok {
		return nil
	}
	return []string{name}
}
