package mitm_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/perimeter/mitm"
)

// mcpProxy builds a proxy with a single MCP-policy host pointing at upstream.
func mcpProxy(t *testing.T, upstreamAddr string, host string, pol mitm.MCPPolicy, extraCfg ...func(*mitm.Config)) (*httptest.Server, *egressCollector) {
	t.Helper()
	ec := &egressCollector{}
	cfg := mitm.Config{
		SandboxID:       newSandboxID(99),
		AllowedHosts:    []string{host},
		Broker:          cred.NewBroker(),
		AllowedBranches: []string{"refs/heads/nexus/**"},
		MCPPolicies:     map[string]mitm.MCPPolicy{host: pol},
		OnEgress:        ec.collect,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, upstreamAddr)
			},
		},
	}
	for _, fn := range extraCfg {
		fn(&cfg)
	}
	p, err := mitm.New(cfg)
	if err != nil {
		t.Fatalf("mitm.New: %v", err)
	}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	return srv, ec
}

type egressCollector struct {
	mu      sync.Mutex
	records []egressRecord
}
type egressRecord struct{ host, verdict, reason string }

func (ec *egressCollector) collect(host, verdict, reason string, _ time.Time) {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	ec.records = append(ec.records, egressRecord{host, verdict, reason})
}
func (ec *egressCollector) denies() []egressRecord {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	var out []egressRecord
	for _, r := range ec.records {
		if r.verdict == "deny" {
			out = append(out, r)
		}
	}
	return out
}

// postMCP sends a POST with application/json body to host via proxy.
func postMCP(t *testing.T, client *http.Client, host, path, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, "http://"+host+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func readBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return b
}

func decodeJSONRPCError(t *testing.T, b []byte) (id json.RawMessage, code int, message string) {
	t.Helper()
	var v struct {
		ID    json.RawMessage `json:"id"`
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode JSON-RPC error: %v (body: %s)", err, b)
	}
	return v.ID, v.Error.Code, v.Error.Message
}

// upstreamEcho returns a server that echoes the request body.
func upstreamEcho(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.Copy(w, r.Body) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv
}

const mcpHost = "mcp.example.test"

// --- ValidateMCPPolicy unit tests ---

func TestValidateMCPPolicy_OK(t *testing.T) {
	t.Parallel()
	err := mitm.ValidateMCPPolicy(mitm.MCPPolicy{
		Allow: []string{"run_code", "read_file"},
		Args: map[string]map[string]string{
			"read_file": {"path": "/workspace/*"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateMCPPolicy_Errors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		pol  mitm.MCPPolicy
	}{
		{"empty tool name", mitm.MCPPolicy{Allow: []string{""}}},
		{"args key not in allow", mitm.MCPPolicy{
			Allow: []string{"run_code"},
			Args:  map[string]map[string]string{"other_tool": {"x": "*"}},
		}},
		{"bad glob", mitm.MCPPolicy{
			Allow: []string{"run_code"},
			Args:  map[string]map[string]string{"run_code": {"path": "[bad"}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := mitm.ValidateMCPPolicy(tc.pol); err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

// --- HTTP integration tests ---

func TestMCP_AllowedToolForwarded(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Allow: []string{"run_code"}}
	proxySrv, _ := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)
	client := proxyClient(proxySrv.URL)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"run_code","arguments":{}}}`
	resp := postMCP(t, client, mcpHost, "/mcp", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	got := string(readBody(t, resp))
	if got != body {
		t.Fatalf("body not forwarded verbatim: got %q", got)
	}
}

func TestMCP_DeniedTool_200WithEchoedID(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Allow: []string{"run_code"}}
	proxySrv, ec := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)
	client := proxyClient(proxySrv.URL)

	body := `{"jsonrpc":"2.0","id":42,"method":"tools/call","params":{"name":"write_file","arguments":{}}}`
	resp := postMCP(t, client, mcpHost, "/mcp", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	id, code, msg := decodeJSONRPCError(t, readBody(t, resp))
	if string(id) != "42" {
		t.Fatalf("want id=42, got %s", id)
	}
	if code != -32001 {
		t.Fatalf("want code -32001, got %d", code)
	}
	if !strings.Contains(msg, "write_file") {
		t.Fatalf("message should mention tool name, got %q", msg)
	}
	if denies := ec.denies(); len(denies) == 0 {
		t.Fatal("want OnEgress deny record")
	}
}

func TestMCP_ArgsGlob_Allow(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{
		Allow: []string{"read_file"},
		Args:  map[string]map[string]string{"read_file": {"path": "/workspace/*"}},
	}
	proxySrv, _ := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)
	client := proxyClient(proxySrv.URL)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"/workspace/foo.txt"}}}`
	resp := postMCP(t, client, mcpHost, "/mcp", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}

func TestMCP_ArgsGlob_Deny(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{
		Allow: []string{"read_file"},
		Args:  map[string]map[string]string{"read_file": {"path": "/workspace/*"}},
	}
	proxySrv, ec := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)
	client := proxyClient(proxySrv.URL)

	body := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"/etc/passwd"}}}`
	resp := postMCP(t, client, mcpHost, "/mcp", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	_, code, msg := decodeJSONRPCError(t, readBody(t, resp))
	if code != -32001 {
		t.Fatalf("want -32001, got %d", code)
	}
	if !strings.Contains(msg, "path") {
		t.Fatalf("message should name the arg, got %q", msg)
	}
	if len(ec.denies()) == 0 {
		t.Fatal("want deny record")
	}
}

func TestMCP_BadJSON_403(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Allow: []string{"run_code"}}
	proxySrv, _ := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)
	client := proxyClient(proxySrv.URL)

	req, _ := http.NewRequest(http.MethodPost, "http://"+mcpHost+"/mcp", strings.NewReader("not-json"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403, got %d", resp.StatusCode)
	}
}

func TestMCP_TextPlainCT_403(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Allow: []string{"run_code"}}
	proxySrv, _ := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)
	client := proxyClient(proxySrv.URL)

	req, _ := http.NewRequest(http.MethodPost, "http://"+mcpHost+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"run_code"}}`))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403, got %d", resp.StatusCode)
	}
}

func TestMCP_BrEncoding_403(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Allow: []string{"run_code"}}
	proxySrv, _ := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)
	client := proxyClient(proxySrv.URL)

	req, _ := http.NewRequest(http.MethodPost, "http://"+mcpHost+"/mcp",
		strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "br")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403, got %d", resp.StatusCode)
	}
}

func gzipBody(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	fmt.Fprint(w, s)
	w.Close()
	return buf.Bytes()
}

func TestMCP_GzipAllowed(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Allow: []string{"run_code"}}
	proxySrv, _ := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"run_code","arguments":{}}}`
	gz := gzipBody(t, body)

	req, _ := http.NewRequest(http.MethodPost, "http://"+mcpHost+"/mcp", bytes.NewReader(gz))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")

	proxyURL, _ := url.Parse(proxySrv.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}, Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}

func TestMCP_GzipDenied(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Allow: []string{"run_code"}}
	proxySrv, _ := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)

	body := `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"write_file","arguments":{}}}`
	gz := gzipBody(t, body)

	req, _ := http.NewRequest(http.MethodPost, "http://"+mcpHost+"/mcp", bytes.NewReader(gz))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")

	proxyURL, _ := url.Parse(proxySrv.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}, Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	_, code, _ := decodeJSONRPCError(t, readBody(t, resp))
	if code != -32001 {
		t.Fatalf("want -32001, got %d", code)
	}
}

func TestMCP_Oversize_403(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Allow: []string{"run_code"}}
	proxySrv, _ := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)
	client := proxyClient(proxySrv.URL)

	big := make([]byte, (1<<20)+2)
	for i := range big {
		big[i] = 'x'
	}
	req, _ := http.NewRequest(http.MethodPost, "http://"+mcpHost+"/mcp", bytes.NewReader(big))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403, got %d", resp.StatusCode)
	}
}

func TestMCP_DuplicateMethodKey_403(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Allow: []string{"run_code"}}
	proxySrv, _ := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)
	client := proxyClient(proxySrv.URL)

	// Duplicate "method" key — parser-differential smuggling attempt.
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","method":"tools/call","params":{"name":"run_code"}}`
	resp := postMCP(t, client, mcpHost, "/mcp", body)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403, got %d", resp.StatusCode)
	}
}

func TestMCP_Batch_AllAllowed(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Allow: []string{"run_code", "read_file"}}
	proxySrv, _ := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)
	client := proxyClient(proxySrv.URL)

	body := `[
		{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"run_code","arguments":{}}},
		{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read_file","arguments":{}}}
	]`
	resp := postMCP(t, client, mcpHost, "/mcp", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}

func TestMCP_Batch_OneDenied_Rejected(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Allow: []string{"run_code"}}
	proxySrv, ec := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)
	client := proxyClient(proxySrv.URL)

	body := `[
		{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"run_code","arguments":{}}},
		{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"write_file","arguments":{}}}
	]`
	resp := postMCP(t, client, mcpHost, "/mcp", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	b := readBody(t, resp)
	var arr []json.RawMessage
	if err := json.Unmarshal(b, &arr); err != nil {
		t.Fatalf("response should be JSON array: %v", err)
	}
	if len(arr) != 2 {
		t.Fatalf("want 2 elements in batch response, got %d", len(arr))
	}
	if len(ec.denies()) == 0 {
		t.Fatal("want deny record")
	}
}

func TestMCP_Initialize_Passes(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Allow: []string{"run_code"}}
	proxySrv, _ := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)
	client := proxyClient(proxySrv.URL)

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	resp := postMCP(t, client, mcpHost, "/mcp", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	got := string(readBody(t, resp))
	if got != body {
		t.Fatalf("want body forwarded verbatim, got %q", got)
	}
}

func TestMCP_ToolsList_Passes(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Allow: []string{"run_code"}}
	proxySrv, _ := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)
	client := proxyClient(proxySrv.URL)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	resp := postMCP(t, client, mcpHost, "/mcp", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}

func TestMCP_Ping_Passes(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Allow: []string{"run_code"}}
	proxySrv, _ := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)
	client := proxyClient(proxySrv.URL)

	body := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	resp := postMCP(t, client, mcpHost, "/mcp", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}

func TestMCP_GET_Passes(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Allow: []string{"run_code"}}
	proxySrv, _ := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)
	client := proxyClient(proxySrv.URL)

	req, _ := http.NewRequest(http.MethodGet, "http://"+mcpHost+"/mcp", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 for GET pass-through, got %d", resp.StatusCode)
	}
}

func TestMCP_PathMismatch_Passes(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Path: "/mcp", Allow: []string{"run_code"}}
	proxySrv, _ := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)
	client := proxyClient(proxySrv.URL)

	// POST to /other should bypass policy since Path is /mcp.
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"write_file","arguments":{}}}`
	req, _ := http.NewRequest(http.MethodPost, "http://"+mcpHost+"/other", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 for path-mismatch pass-through, got %d", resp.StatusCode)
	}
	got := string(readBody(t, resp))
	if got != body {
		t.Fatalf("want body forwarded verbatim, got %q", got)
	}
}

func TestMCP_AllowAll_StillMITMsMCPHost(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Allow: []string{"run_code"}}

	ec := &egressCollector{}
	cfg := mitm.Config{
		SandboxID:       newSandboxID(101),
		AllowAll:        true,
		Broker:          cred.NewBroker(),
		AllowedBranches: []string{"refs/heads/nexus/**"},
		MCPPolicies:     map[string]mitm.MCPPolicy{mcpHost: pol},
		OnEgress:        ec.collect,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
			},
		},
	}
	p, err := mitm.New(cfg)
	if err != nil {
		t.Fatalf("mitm.New: %v", err)
	}
	proxySrv := httptest.NewServer(p)
	t.Cleanup(proxySrv.Close)
	client := proxyClient(proxySrv.URL)

	// Denied tool should be caught even with AllowAll=true.
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"write_file","arguments":{}}}`
	resp := postMCP(t, client, mcpHost, "/mcp", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	_, code, _ := decodeJSONRPCError(t, readBody(t, resp))
	if code != -32001 {
		t.Fatalf("want -32001, got %d", code)
	}
	if len(ec.denies()) == 0 {
		t.Fatal("want deny record for AllowAll + MCP host")
	}
}

func TestMCP_ToolsListResponse_JSONFiltered(t *testing.T) {
	t.Parallel()
	pol := mitm.MCPPolicy{Allow: []string{"run_code"}}

	// Upstream returns a tools/list response with run_code and write_file.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"run_code"},{"name":"write_file"}]}}`
		w.Write([]byte(resp)) //nolint:errcheck
	}))
	t.Cleanup(upstream.Close)

	proxySrv, _ := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)
	client := proxyClient(proxySrv.URL)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	resp := postMCP(t, client, mcpHost, "/mcp", body)
	b := readBody(t, resp)

	var result struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatalf("parse response: %v (body: %s)", err, b)
	}
	if len(result.Result.Tools) != 1 || result.Result.Tools[0].Name != "run_code" {
		t.Fatalf("expected only run_code in tools list, got %+v", result.Result.Tools)
	}
}

func TestMCP_ToolsListResponse_SSEFiltered(t *testing.T) {
	t.Parallel()
	pol := mitm.MCPPolicy{Allow: []string{"run_code"}}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		line := `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"run_code"},{"name":"write_file"}]}}`
		fmt.Fprintf(w, "data: %s\n\n", line)
	}))
	t.Cleanup(upstream.Close)

	proxySrv, _ := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)
	client := proxyClient(proxySrv.URL)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	resp := postMCP(t, client, mcpHost, "/mcp", body)
	b := readBody(t, resp)

	// The SSE data line should have write_file removed.
	s := string(b)
	if strings.Contains(s, "write_file") {
		t.Fatalf("write_file should be filtered from SSE response, got: %s", s)
	}
	if !strings.Contains(s, "run_code") {
		t.Fatalf("run_code should remain in SSE response, got: %s", s)
	}
}

func TestMCP_NonMCPHost_NotAffected(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Allow: []string{"run_code"}}
	// Policy is on mcpHost; request goes to a different host.
	other := "other.example.test"
	cfg := mitm.Config{
		SandboxID:       newSandboxID(103),
		AllowedHosts:    []string{mcpHost, other},
		Broker:          cred.NewBroker(),
		AllowedBranches: []string{"refs/heads/nexus/**"},
		MCPPolicies:     map[string]mitm.MCPPolicy{mcpHost: pol},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
			},
		},
	}
	p, _ := mitm.New(cfg)
	proxySrv := httptest.NewServer(p)
	t.Cleanup(proxySrv.Close)
	client := proxyClient(proxySrv.URL)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"write_file","arguments":{}}}`
	req, _ := http.NewRequest(http.MethodPost, "http://"+other+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("non-MCP host should pass, got %d", resp.StatusCode)
	}
	got := string(readBody(t, resp))
	if got != body {
		t.Fatalf("body should be forwarded verbatim to non-MCP host, got %q", got)
	}
}

func TestMCP_EmptyBatch_403(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Allow: []string{"run_code"}}
	proxySrv, _ := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)
	client := proxyClient(proxySrv.URL)

	resp := postMCP(t, client, mcpHost, "/mcp", `[]`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403 for empty batch, got %d", resp.StatusCode)
	}
}

func TestMCP_SandboxID_Uniqueness(t *testing.T) {
	// Verify that two proxies with different SandboxIDs can have the same
	// MCPPolicy without interfering.
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Allow: []string{"run_code"}}

	make := func(id byte) *httptest.Server {
		cfg := mitm.Config{
			SandboxID:       newSandboxID(id),
			AllowedHosts:    []string{mcpHost},
			Broker:          cred.NewBroker(),
			AllowedBranches: []string{"refs/heads/nexus/**"},
			MCPPolicies:     map[string]mitm.MCPPolicy{mcpHost: pol},
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
				},
			},
		}
		p, _ := mitm.New(cfg)
		srv := httptest.NewServer(p)
		t.Cleanup(srv.Close)
		return srv
	}

	p1, p2 := make(10), make(11)
	c1, c2 := proxyClient(p1.URL), proxyClient(p2.URL)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"run_code","arguments":{}}}`

	for _, c := range []*http.Client{c1, c2} {
		resp := postMCP(t, c, mcpHost, "/mcp", body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("want 200, got %d", resp.StatusCode)
		}
	}
}

// TestMCP_SandboxID_domain verifies that the domain.SandboxID zero value works
// (used by tests that do not need isolation).
func TestMCP_ZeroSandboxID_OK(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Allow: []string{"run_code"}}
	cfg := mitm.Config{
		SandboxID:       domain.SandboxID{},
		AllowedHosts:    []string{mcpHost},
		Broker:          cred.NewBroker(),
		AllowedBranches: []string{"refs/heads/nexus/**"},
		MCPPolicies:     map[string]mitm.MCPPolicy{mcpHost: pol},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
			},
		},
	}
	p, _ := mitm.New(cfg)
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	client := proxyClient(srv.URL)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"run_code","arguments":{}}}`
	resp := postMCP(t, client, mcpHost, "/mcp", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}

// TestMCP_CaseVariantMethod ensures struct-fold bypasses are blocked.
// {"method":"tools/call","Method":"ping"} must be denied even though Method≠method
// for hasDuplicateTopLevelJSONKeys; the exact-key map decode sees "tools/call".
func TestMCP_CaseVariantMethod_Denied(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Allow: []string{"run_code"}}
	proxySrv, ec := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)
	client := proxyClient(proxySrv.URL)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","Method":"ping","params":{"name":"publish_zip","arguments":{}}}`
	resp := postMCP(t, client, mcpHost, "/mcp", body)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403 (fail-closed for fold-ambiguous method key), got %d", resp.StatusCode)
	}
	if len(ec.denies()) == 0 {
		t.Fatal("want deny record for case-variant method bypass")
	}
}

// TestMCP_CaseVariantName ensures a case-variant "Name" key does not bypass the tool allow-list.
func TestMCP_CaseVariantName_Denied(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Allow: []string{"run_code"}}
	proxySrv, ec := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)
	client := proxyClient(proxySrv.URL)

	body := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"write_file","Name":"run_code","arguments":{}}}`
	resp := postMCP(t, client, mcpHost, "/mcp", body)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403 (fail-closed for fold-ambiguous name key), got %d", resp.StatusCode)
	}
	if len(ec.denies()) == 0 {
		t.Fatal("want deny record for case-variant name bypass")
	}
}

// --- fold-key bypass probes ---

func TestMCP_FoldKey_MethodOnly_Denied(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Allow: []string{"read"}}
	proxySrv, _ := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)
	client := proxyClient(proxySrv.URL)

	body := `{"jsonrpc":"2.0","id":1,"Method":"tools/call","params":{"name":"publish"}}`
	resp := postMCP(t, client, mcpHost, "/mcp", body)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403 for fold-ambiguous Method key, got %d", resp.StatusCode)
	}
}

func TestMCP_FoldKey_PingAndMETHOD_Denied(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Allow: []string{"read"}}
	proxySrv, _ := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)
	client := proxyClient(proxySrv.URL)

	body := `{"jsonrpc":"2.0","id":1,"method":"ping","METHOD":"tools/call","params":{"name":"publish"}}`
	resp := postMCP(t, client, mcpHost, "/mcp", body)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403 for fold-ambiguous METHOD key, got %d", resp.StatusCode)
	}
}

func TestMCP_FoldKey_NameAndNAME_Denied(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Allow: []string{"read"}}
	proxySrv, _ := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)
	client := proxyClient(proxySrv.URL)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read","NAME":"publish"}}`
	resp := postMCP(t, client, mcpHost, "/mcp", body)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403 for fold-ambiguous NAME key, got %d", resp.StatusCode)
	}
}

func TestMCP_FoldKey_LongS_Denied(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Allow: []string{"read"}}
	proxySrv, _ := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)
	client := proxyClient(proxySrv.URL)

	// U+017F long-s: strings.EqualFold("paramſ","params") == true
	body := "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"paramſ\":{\"name\":\"publish\"}}"
	resp := postMCP(t, client, mcpHost, "/mcp", body)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403 for long-s fold bypass, got %d", resp.StatusCode)
	}
}

func TestMCP_FoldKey_KelvinSign_Denied(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Allow: []string{"read"}}
	proxySrv, _ := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)
	client := proxyClient(proxySrv.URL)

	// "JSONRPC" EqualFolds to "jsonrpc"
	body := "{\"jsonrpc\":\"2.0\",\"JSONRPC\":\"extra\",\"id\":1,\"method\":\"ping\"}"
	resp := postMCP(t, client, mcpHost, "/mcp", body)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403 for fold-ambiguous JSONRPC key, got %d", resp.StatusCode)
	}
}

// --- path-spelling bypass tests ---

func TestMCP_PathSpelling_UpperCase_Denied(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Path: "/mcp", Allow: []string{"run_code"}}
	proxySrv, _ := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)
	client := proxyClient(proxySrv.URL)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"publish","arguments":{}}}`
	req, _ := http.NewRequest(http.MethodPost, "http://"+mcpHost+"/MCP", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	b := readBody(t, resp)
	_, code, _ := decodeJSONRPCError(t, b)
	if code != -32001 {
		t.Fatalf("want JSON-RPC -32001 for uppercase /MCP (inspected via EqualFold match), got code %d body %s", code, b)
	}
}

func TestMCP_PathSpelling_DoubleSlash_Denied(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Path: "/mcp", Allow: []string{"run_code"}}
	proxySrv, _ := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)
	client := proxyClient(proxySrv.URL)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"publish","arguments":{}}}`
	req, _ := http.NewRequest(http.MethodPost, "http://"+mcpHost+"//mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403 for double-slash path //mcp, got %d", resp.StatusCode)
	}
}

func TestMCP_PathSpelling_PercentEncoded_Denied(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Path: "/mcp", Allow: []string{"run_code"}}
	proxySrv, _ := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)
	client := proxyClient(proxySrv.URL)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"publish","arguments":{}}}`
	req, _ := http.NewRequest(http.MethodPost, "http://"+mcpHost+"/%6dcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403 for percent-encoded path, got %d", resp.StatusCode)
	}
}

func TestMCP_PathTrailingSlash_Passes(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Path: "/mcp", Allow: []string{"run_code"}}
	proxySrv, _ := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)
	client := proxyClient(proxySrv.URL)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"run_code","arguments":{}}}`
	req, _ := http.NewRequest(http.MethodPost, "http://"+mcpHost+"/mcp/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 for /mcp/ matching Path=/mcp, got %d", resp.StatusCode)
	}
}

// --- method coverage tests ---

func TestMCP_PUT_Inspected(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Allow: []string{"read"}}
	proxySrv, ec := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)
	client := proxyClient(proxySrv.URL)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"publish","arguments":{}}}`
	req, _ := http.NewRequest(http.MethodPut, "http://"+mcpHost+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	b := readBody(t, resp)
	_, code, _ := decodeJSONRPCError(t, b)
	if code != -32001 {
		t.Fatalf("want JSON-RPC -32001 for PUT with denied tool (PUT must be inspected), got code %d body %s", code, b)
	}
	if len(ec.denies()) == 0 {
		t.Fatal("want deny record for PUT with denied tool")
	}
}

func TestMCP_LowercasePost_Inspected(t *testing.T) {
	t.Parallel()
	upstream := upstreamEcho(t)
	pol := mitm.MCPPolicy{Allow: []string{"read"}}
	proxySrv, ec := mcpProxy(t, upstream.Listener.Addr().String(), mcpHost, pol)
	client := proxyClient(proxySrv.URL)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"publish","arguments":{}}}`
	req, _ := http.NewRequest("post", "http://"+mcpHost+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	b := readBody(t, resp)
	_, code, _ := decodeJSONRPCError(t, b)
	if code != -32001 {
		t.Fatalf("want JSON-RPC -32001 for lowercase-method post (must be inspected), got code %d body %s", code, b)
	}
	if len(ec.denies()) == 0 {
		t.Fatal("want deny record for lowercase post method")
	}
}

// --- ValidateMCPPolicy and New tests ---

func TestMCP_ValidateMCPPolicy_PathNoSlash(t *testing.T) {
	pol := mitm.MCPPolicy{Path: "mcp", Allow: []string{"read"}}
	if err := mitm.ValidateMCPPolicy(pol); err == nil {
		t.Fatal("want error for Path not starting with /")
	}
}

func TestMCP_New_TrailingDotNormalized(t *testing.T) {
	t.Parallel()
	pol := mitm.MCPPolicy{Allow: []string{"run_code"}}
	cfg := mitm.Config{
		SandboxID:       newSandboxID(200),
		AllowAll:        true,
		Broker:          cred.NewBroker(),
		AllowedBranches: []string{"refs/heads/nexus/**"},
		MCPPolicies:     map[string]mitm.MCPPolicy{"mcp.test.": pol},
	}
	_, err := mitm.New(cfg)
	if err != nil {
		t.Fatalf("New with trailing-dot key must succeed, got: %v", err)
	}
}

func TestMCP_New_InvalidPolicy_Error(t *testing.T) {
	pol := mitm.MCPPolicy{Allow: []string{}}
	cfg := mitm.Config{
		SandboxID:       newSandboxID(201),
		AllowAll:        true,
		Broker:          cred.NewBroker(),
		AllowedBranches: []string{"refs/heads/nexus/**"},
		MCPPolicies:     map[string]mitm.MCPPolicy{"mcp.test": pol},
	}
	_, err := mitm.New(cfg)
	if err == nil {
		t.Fatal("want error for invalid policy in New")
	}
}
