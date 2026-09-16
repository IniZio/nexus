package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/portfwd"
)

func fakeHerdrSocket(t *testing.T) (sockPath string, received <-chan []byte) {
	t.Helper()
	sockPath = filepath.Join(t.TempDir(), "herdr.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	ch := make(chan []byte, 4)
	go func() {
		defer ln.Close()
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(2 * time.Second)) //nolint:errcheck
		scanner := bufio.NewScanner(conn)
		if scanner.Scan() {
			b := make([]byte, len(scanner.Bytes()))
			copy(b, scanner.Bytes())
			ch <- b
		}
		conn.Write([]byte("{\"id\":1,\"result\":{}}\n")) //nolint:errcheck
	}()
	t.Cleanup(func() { ln.Close() })
	return sockPath, ch
}

func seedMetadataBinding(t *testing.T, storeRoot, workspaceID, sandboxHandle, sandboxID string) {
	t.Helper()
	b := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:" + sandboxID,
		HerdrWorkspaceID: workspaceID,
		SandboxHandle:    sandboxHandle,
		SandboxID:        sandboxID,
	}
	if err := HerdrSpacePut(context.Background(), storeRoot, b); err != nil {
		t.Fatalf("seedMetadataBinding: %v", err)
	}
}

func TestReportForwardStatus_PortsPresent_SendsSortedList(t *testing.T) {
	sockPath, received := fakeHerdrSocket(t)
	dir := t.TempDir()
	storeRoot := filepath.Join(dir, "store")
	stateDir := filepath.Join(dir, "state")

	seedMetadataBinding(t, storeRoot, "wTest", "test/sb1", "sb1")

	entries := []portfwd.Entry{
		{Port: 5173, Sandbox: "test/sb1", Status: "live", ConfirmedAt: time.Now()},
		{Port: 3000, Sandbox: "test/sb1", Status: "live", ConfirmedAt: time.Now()},
	}
	if err := portfwd.WriteSandboxState(stateDir, "sb1", entries, time.Now()); err != nil {
		t.Fatalf("write state: %v", err)
	}

	if err := herdrReportForwardStatus(context.Background(), "wTest", "", stateDir, storeRoot, sockPath, io.Discard); err != nil {
		t.Fatalf("herdrReportForwardStatus: %v", err)
	}

	select {
	case msg := <-received:
		var req map[string]interface{}
		if err := json.Unmarshal(msg, &req); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if req["method"] != "workspace.report_metadata" {
			t.Errorf("method = %v, want workspace.report_metadata", req["method"])
		}
		params, _ := req["params"].(map[string]interface{})
		if params["source"] != "plugin:nexus3" {
			t.Errorf("source = %v, want plugin:nexus3", params["source"])
		}
		tokens, _ := params["tokens"].(map[string]interface{})
		if tokens["port_forward_status"] != "3000,5173" {
			t.Errorf("port_forward_status = %v, want 3000,5173", tokens["port_forward_status"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no message received from fake socket within 2s")
	}
}

func TestFocusChanged_BoundWorkspace_SendsReportMetadataBeforeReturn(t *testing.T) {
	sockPath, received := fakeHerdrSocket(t)
	dir := t.TempDir()
	storeRoot := filepath.Join(dir, "store")
	stateDir := filepath.Join(dir, "state")
	statePath := filepath.Join(dir, "focus.state")

	seedMetadataBinding(t, storeRoot, "wFocus", "focus/sb1", "sbfocus1")

	entries := []portfwd.Entry{
		{Port: 8080, Sandbox: "focus/sb1", Status: "live", ConfirmedAt: time.Now()},
	}
	if err := portfwd.WriteSandboxState(stateDir, "sbfocus1", entries, time.Now()); err != nil {
		t.Fatalf("write state: %v", err)
	}

	if err := herdrFocusChanged(context.Background(), "wFocus", false, storeRoot, statePath, stateDir, "", sockPath, io.Discard); err != nil {
		t.Fatalf("herdrFocusChanged: %v", err)
	}

	select {
	case msg := <-received:
		var req map[string]any
		if err := json.Unmarshal(msg, &req); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if req["method"] != "workspace.report_metadata" {
			t.Errorf("method = %v, want workspace.report_metadata", req["method"])
		}
		params, _ := req["params"].(map[string]any)
		if params["source"] != "plugin:nexus3" {
			t.Errorf("source = %v, want plugin:nexus3", params["source"])
		}
		tokens, _ := params["tokens"].(map[string]any)
		if tokens["port_forward_status"] != "8080" {
			t.Errorf("port_forward_status = %v, want 8080", tokens["port_forward_status"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no workspace.report_metadata received within 2s of herdrFocusChanged returning")
	}
}

func TestReportForwardStatus_Empty_NullToken(t *testing.T) {
	sockPath, received := fakeHerdrSocket(t)
	dir := t.TempDir()
	storeRoot := filepath.Join(dir, "store")
	stateDir := filepath.Join(dir, "state")

	seedMetadataBinding(t, storeRoot, "wEmpty", "test/sb2", "sb2")

	if err := herdrReportForwardStatus(context.Background(), "wEmpty", "", stateDir, storeRoot, sockPath, io.Discard); err != nil {
		t.Fatalf("herdrReportForwardStatus: %v", err)
	}

	select {
	case msg := <-received:
		var req map[string]interface{}
		if err := json.Unmarshal(msg, &req); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		params, _ := req["params"].(map[string]interface{})
		tokens, _ := params["tokens"].(map[string]interface{})
		if v, ok := tokens["port_forward_status"]; !ok || v != nil {
			t.Errorf("port_forward_status = %v (ok=%v), want null", v, ok)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no message received from fake socket within 2s")
	}
}
