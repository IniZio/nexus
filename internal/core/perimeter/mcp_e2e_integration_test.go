//go:build integration

// TestMCPEgress_GuestOnWire_E2E proves per-host MCP tool-call egress policy
// from inside a real VM guest:
//
//  1. tools/call get_artifact (allowed) → HTTP 200, stub saw the call.
//  2. tools/call publish_zip (denied)   → HTTP 200 + JSON-RPC -32001; stub never saw it.
//  3. tools/list                         → stub sends 3 tools; guest receives only 2 (filtered).
//  4. text/plain Content-Type POST       → HTTP 403 fail-closed; stub never saw it.
//  5. deny events appear in egress-decisions.jsonl, verdict=deny, reason mcp:*.
//
// Guard conditions mirror TestEgress_GuestOnWire_E2E; same boot artifacts required.
package perimeter_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus/internal/core/perimeter"
	"github.com/IniZio/nexus/internal/core/perimeter/mitm"
	"github.com/IniZio/nexus/internal/core/perimeter/netfilter"
	"github.com/IniZio/nexus/internal/core/perimeter/netstack"
)

// TestMCPEgress_GuestOnWire_E2E boots a real VM and asserts MCP policy enforcement.
func TestMCPEgress_GuestOnWire_E2E(t *testing.T) {
	// ── guard: /dev/kvm ────────────────────────────────────────────────────────
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("skipping: /dev/kvm not present")
	}
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("skipping: /dev/kvm not usable: %v", err)
	}
	f.Close()

	if data, err := os.ReadFile("/proc/sys/kernel/unprivileged_userns_clone"); err == nil {
		if strings.TrimSpace(string(data)) == "0" {
			t.Skip("skipping: unprivileged_userns_clone=0")
		}
	}

	chBin := os.Getenv("CLOUD_HYPERVISOR_BIN")
	if chBin == "" {
		chBin = filepath.Join(os.Getenv("HOME"), ".local/bin/cloud-hypervisor")
	}
	if _, err := os.Stat(chBin); err != nil {
		t.Skipf("skipping: cloud-hypervisor binary not found at %s", chBin)
	}

	kernelPath := e2eSkipUnlessArtifact(t, "vmlinux-x86_64")
	baseInitramfs := e2eSkipUnlessArtifact(t, "alpine-initramfs.cpio.gz")

	assertCapEffClear(t, "pre-test")

	socketDir, err := os.MkdirTemp("/tmp", "nx-mcp-e2e-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })
	if len(socketDir)+35 > 107 {
		t.Skipf("socket path too long for Unix socket: %s", socketDir)
	}

	id := domain.NewSandboxID()

	// ── stub MCP TLS server ────────────────────────────────────────────────────
	// stubCalls captures tool names that reach the stub; blocked calls never arrive.
	stubCalls := make(chan string, 32)
	stub := httptest.NewTLSServer(http.HandlerFunc(mcpStubHandler(stubCalls)))
	t.Cleanup(stub.Close)

	stubTransport := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, stub.Listener.Addr().String())
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec — test stub redirect
	}

	// ── egress-decisions.jsonl sink ────────────────────────────────────────────
	decisionsPath := filepath.Join(socketDir, "egress-decisions.jsonl")
	df, err := os.Create(decisionsPath)
	if err != nil {
		t.Fatalf("create egress-decisions.jsonl: %v", err)
	}
	var egressMu sync.Mutex
	egressEnc := json.NewEncoder(df)
	type egressRec struct {
		Host    string `json:"host"`
		Verdict string `json:"verdict"`
		Reason  string `json:"reason"`
	}
	onEgress := func(host, verdict, reason string, _ time.Time) {
		egressMu.Lock()
		defer egressMu.Unlock()
		_ = egressEnc.Encode(egressRec{Host: host, Verdict: verdict, Reason: reason})
	}

	// ── MITM proxy with MCP policy for mcp.test ───────────────────────────────
	const mcpHost = "mcp.test"
	const mcpIP = "10.0.0.60"
	proxy, err := mitm.New(mitm.Config{
		SandboxID: id,
		Transport: stubTransport,
		OnEgress:  onEgress,
		MCPPolicies: map[string]mitm.MCPPolicy{
			mcpHost: {Allow: []string{"get_artifact", "list_artifacts"}},
		},
	})
	if err != nil {
		t.Fatalf("mitm.New: %v", err)
	}

	al, err := netfilter.NewAllowList([]string{mcpIP}, nil, nil)
	if err != nil {
		t.Fatalf("NewAllowList: %v", err)
	}

	initScript := mcpBuildInitScript(mcpIP, mcpHost)
	combinedInitramfs := e2eBuildInitramfs(t, baseInitramfs, initScript)

	serialPath := filepath.Join(socketDir, "serial.txt")

	drv, err := cloudhypervisor.New(cloudhypervisor.Config{
		BinaryPath:       chBin,
		SocketDir:        socketDir,
		KernelPath:       kernelPath,
		InitramfsPath:    combinedInitramfs,
		Cmdline:          "console=ttyS0 panic=5",
		VCPUs:            1,
		MemoryMiB:        256,
		StartTimeout:     30 * time.Second,
		SerialOutputPath: serialPath,
	})
	if err != nil {
		t.Fatalf("cloudhypervisor.New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)

	t.Log("booting VM…")
	_, err = drv.Start(ctx, driver.StartRequest{SandboxID: id})
	if err != nil {
		t.Fatalf("drv.Start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer stopCancel()
		_ = drv.Stop(stopCtx, id)
	})

	fd, err := drv.GuestNetworkFD(ctx, id)
	if err != nil {
		t.Fatalf("GuestNetworkFD: %v", err)
	}

	stack := netstack.New(al, nil)
	_, err = perimeter.Start(ctx, id, fd, stack, proxy, al)
	if err != nil {
		t.Fatalf("perimeter.Start: %v", err)
	}

	pollSerialLine(t, serialPath, "nexus_init_start:", 20*time.Second)
	pollSerialLine(t, serialPath, "nexus_dhcp_done:", 40*time.Second)

	// ── assertion 1: allowed tool call reaches stub ────────────────────────────
	t.Log("case 1: get_artifact (allowed)…")
	allowExit := pollSerialLine(t, serialPath, "nexus_mcp_allow_exit:", 60*time.Second)
	if strings.TrimSpace(allowExit) != "0" {
		t.Errorf("case 1: wget exit %q, want 0 (HTTP 200)", allowExit)
	}
	select {
	case got := <-stubCalls:
		if got != "get_artifact" {
			t.Errorf("case 1: stub saw tool %q, want get_artifact", got)
		} else {
			t.Log("PASS case 1: stub saw get_artifact")
		}
	case <-time.After(5 * time.Second):
		t.Error("FAIL case 1: stub never received get_artifact")
	}

	// ── assertion 2: denied tool call blocked; HTTP 200 with -32001 ───────────
	t.Log("case 2: publish_zip (denied)…")
	denyExit := pollSerialLine(t, serialPath, "nexus_mcp_deny_exit:", 30*time.Second)
	if strings.TrimSpace(denyExit) != "0" {
		t.Errorf("case 2: wget exit %q, want 0 (policy-deny returns HTTP 200)", denyExit)
	}
	denyHasErr := pollSerialLine(t, serialPath, "nexus_mcp_deny_has_err:", 5*time.Second)
	if strings.TrimSpace(denyHasErr) != "0" {
		t.Errorf("case 2: -32001 absent from deny response (grep exit %q)", denyHasErr)
	} else {
		t.Log("PASS case 2a: response body contains -32001")
	}
	select {
	case got := <-stubCalls:
		t.Errorf("FAIL case 2: stub received %q — should have been blocked", got)
	case <-time.After(3 * time.Second):
		t.Log("PASS case 2b: stub never saw publish_zip")
	}

	// ── assertion 3: tools/list response is filtered ───────────────────────────
	t.Log("case 3: tools/list (filtered)…")
	pollSerialLine(t, serialPath, "nexus_mcp_list_exit:", 30*time.Second)

	if v := pollSerialLine(t, serialPath, "nexus_mcp_list_get_artifact:", 5*time.Second); strings.TrimSpace(v) != "0" {
		t.Errorf("case 3: get_artifact absent from filtered tools/list (grep exit %q)", v)
	} else {
		t.Log("PASS case 3a: get_artifact present in filtered tools/list")
	}
	// publish_zip must be absent: grep exits 1 (not found) meaning filtering worked.
	if v := pollSerialLine(t, serialPath, "nexus_mcp_list_publish_zip:", 5*time.Second); strings.TrimSpace(v) != "1" {
		t.Errorf("case 3: publish_zip still in tools/list (grep exit %q, want 1=absent)", v)
	} else {
		t.Log("PASS case 3b: publish_zip absent from filtered tools/list")
	}

	// ── assertion 4: text/plain → 403 fail-closed ─────────────────────────────
	t.Log("case 4: text/plain (fail-closed)…")
	badctExit := pollSerialLine(t, serialPath, "nexus_mcp_badct_exit:", 30*time.Second)
	if strings.TrimSpace(badctExit) == "0" {
		t.Errorf("FAIL case 4: wget exited 0 on 403 fail-closed; expected non-zero")
	} else {
		t.Logf("PASS case 4: wget exit %q (non-zero) on 403", badctExit)
	}
	select {
	case got := <-stubCalls:
		t.Errorf("FAIL case 4: stub received %q — text/plain should be blocked", got)
	case <-time.After(2 * time.Second):
		t.Log("PASS case 4: stub never saw text/plain request")
	}

	// ── assertion 5: egress-decisions.jsonl has deny entry with mcp: reason ───
	pollSerialLine(t, serialPath, "nexus_mcp_done:", 10*time.Second)
	df.Close()

	data, err := os.ReadFile(decisionsPath)
	if err != nil {
		t.Fatalf("read %s: %v", decisionsPath, err)
	}
	var foundDenyMCP bool
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		var rec egressRec
		if json.Unmarshal(scanner.Bytes(), &rec) != nil {
			continue
		}
		if rec.Verdict == "deny" && strings.HasPrefix(rec.Reason, "mcp:") {
			foundDenyMCP = true
			t.Logf("PASS case 5: deny entry host=%s reason=%s", rec.Host, rec.Reason)
			break
		}
	}
	if !foundDenyMCP {
		t.Errorf("FAIL case 5: no deny/mcp: entry in egress-decisions.jsonl\ncontent:\n%s", string(data))
	}
}

// mcpBuildInitScript returns the guest /init shell script for all 4 MCP probe cases.
func mcpBuildInitScript(mcpIP, mcpHost string) string {
	// Format args: mcpIP, mcpHost (for /etc/hosts), then mcpHost×4 (for each wget URL).
	return fmt.Sprintf(`#!/bin/sh
echo "nexus_init_start:1"
mount -t devtmpfs devtmpfs /dev  2>/dev/null || true
mount -t proc     proc     /proc 2>/dev/null || true
mount -t sysfs    sysfs    /sys  2>/dev/null || true

iface=""
for i in $(ls /sys/class/net/ 2>/dev/null); do
    [ "$i" = "lo" ] && continue
    iface="$i"
    break
done
[ -z "$iface" ] && iface=eth0
ip link set "$iface" up 2>/dev/null || ifconfig "$iface" up 2>/dev/null
/sbin/udhcpc -i "$iface" -n -q -t 20 2>/dev/null
echo "nexus_dhcp_done:$?"

echo "%s %s" >> /etc/hosts

# Case 1: allowed tool — stub must receive it.
wget --no-check-certificate -q \
     --header "Content-Type: application/json" \
     --post-data '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_artifact","arguments":{}}}' \
     -O /dev/null \
     "https://%s/mcp"
echo "nexus_mcp_allow_exit:$?"

# Case 2: denied tool — HTTP 200 + JSON-RPC -32001; stub never sees it.
wget --no-check-certificate -q \
     --header "Content-Type: application/json" \
     --post-data '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"publish_zip","arguments":{}}}' \
     -O /tmp/deny_resp.json \
     "https://%s/mcp"
echo "nexus_mcp_deny_exit:$?"
grep -q '32001' /tmp/deny_resp.json 2>/dev/null
echo "nexus_mcp_deny_has_err:$?"

# Case 3: tools/list — proxy filters response to allowed tools only.
wget --no-check-certificate -q \
     --header "Content-Type: application/json" \
     --post-data '{"jsonrpc":"2.0","id":3,"method":"tools/list"}' \
     -O /tmp/list_resp.json \
     "https://%s/mcp"
echo "nexus_mcp_list_exit:$?"
grep -q 'get_artifact' /tmp/list_resp.json 2>/dev/null
echo "nexus_mcp_list_get_artifact:$?"
grep -q 'publish_zip' /tmp/list_resp.json 2>/dev/null
echo "nexus_mcp_list_publish_zip:$?"

# Case 4: text/plain Content-Type — 403 fail-closed; wget must exit non-zero.
wget --no-check-certificate -q \
     --header "Content-Type: text/plain" \
     --post-data 'plain text body' \
     -O /dev/null \
     "https://%s/mcp"
echo "nexus_mcp_badct_exit:$?"

echo "nexus_mcp_done:0"
while true; do sleep 10; done
`, mcpIP, mcpHost, mcpHost, mcpHost, mcpHost, mcpHost)
}

// mcpStubHandler serves MCP JSON-RPC, sending tool call names to stubCalls.
func mcpStubHandler(stubCalls chan<- string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var req map[string]json.RawMessage
		if json.Unmarshal(body, &req) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var method string
		_ = json.Unmarshal(req["method"], &method)
		id := req["id"]

		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)

		switch method {
		case "initialize":
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{
					"protocolVersion": "2024-11-05",
					"capabilities":    map[string]any{},
					"serverInfo":      map[string]any{"name": "mcp-stub"},
				},
			})
		case "tools/list":
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{
					"tools": []map[string]any{
						{"name": "get_artifact", "description": "get artifact", "inputSchema": map[string]any{"type": "object"}},
						{"name": "list_artifacts", "description": "list artifacts", "inputSchema": map[string]any{"type": "object"}},
						{"name": "publish_zip", "description": "publish zip", "inputSchema": map[string]any{"type": "object"}},
					},
				},
			})
		case "tools/call":
			var params map[string]json.RawMessage
			_ = json.Unmarshal(req["params"], &params)
			var name string
			_ = json.Unmarshal(params["name"], &name)
			select {
			case stubCalls <- name:
			default:
			}
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": "echo:" + name}},
				},
			})
		default:
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": nil})
		}
	}
}
