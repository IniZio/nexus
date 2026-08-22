package cloudhypervisor

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// TestBalloonConfig_FreePageReporting verifies that a vmConfig with
// FreePageReporting=true (and BalloonMiB=0) marshals the expected balloon JSON:
// size=0, deflate_on_oom=true, free_page_reporting=true.
// This exercises the "zero-size balloon for passive reclaim only" path.
func TestBalloonConfig_FreePageReporting(t *testing.T) {
	cfg := vmConfig{
		Payload: vmPayloadConfig{Kernel: "/kernel"},
		Memory:  &vmMemoryConfig{SizeBytes: 512 * 1024 * 1024},
		Balloon: &balloonConfig{
			SizeBytes:         0,
			DeflateOnOOM:      true,
			FreePageReporting: true,
		},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v; raw: %s", err, b)
	}

	balloon, ok := m["balloon"].(map[string]any)
	if !ok {
		t.Fatalf("balloon field missing or wrong type in JSON: %s", b)
	}
	// size=0 must be present (it is the required BalloonConfig field).
	if v, _ := balloon["size"].(float64); v != 0 {
		t.Errorf("balloon.size = %v, want 0", v)
	}
	// deflate_on_oom=true: omitempty only suppresses false, so it should appear.
	if v, _ := balloon["deflate_on_oom"].(bool); !v {
		t.Errorf("balloon.deflate_on_oom = %v, want true", v)
	}
	// free_page_reporting=true: same — should appear.
	if v, _ := balloon["free_page_reporting"].(bool); !v {
		t.Errorf("balloon.free_page_reporting = %v, want true", v)
	}
}

// TestBalloonConfig_BalloonMiB verifies that BalloonMiB > 0 is converted to
// bytes correctly and that free_page_reporting is absent when false.
func TestBalloonConfig_BalloonMiB(t *testing.T) {
	const wantMiB = 256
	cfg := vmConfig{
		Payload: vmPayloadConfig{Kernel: "/kernel"},
		Memory:  &vmMemoryConfig{SizeBytes: 512 * 1024 * 1024},
		Balloon: &balloonConfig{
			SizeBytes:    wantMiB * 1024 * 1024,
			DeflateOnOOM: true,
			// FreePageReporting deliberately left false.
		},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v; raw: %s", err, b)
	}

	balloon, ok := m["balloon"].(map[string]any)
	if !ok {
		t.Fatalf("balloon field missing in JSON: %s", b)
	}
	const wantBytes = float64(wantMiB * 1024 * 1024)
	if v, _ := balloon["size"].(float64); v != wantBytes {
		t.Errorf("balloon.size = %v, want %v (256 MiB in bytes)", v, wantBytes)
	}
	// free_page_reporting is false; omitempty must suppress it.
	if _, present := balloon["free_page_reporting"]; present {
		t.Error("free_page_reporting present in JSON when false; omitempty should suppress it")
	}
}

// TestBalloonConfig_NoBalloon verifies that when Balloon is nil the balloon
// field is absent from the marshaled JSON (omitempty on vmConfig.Balloon).
func TestBalloonConfig_NoBalloon(t *testing.T) {
	cfg := vmConfig{
		Payload: vmPayloadConfig{Kernel: "/kernel"},
		Memory:  &vmMemoryConfig{SizeBytes: 512 * 1024 * 1024},
		// Balloon intentionally omitted.
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v; raw: %s", err, b)
	}
	if _, ok := m["balloon"]; ok {
		t.Errorf("balloon field present in JSON when no balloon configured; full JSON: %s", b)
	}
}

// TestClient_VMResize_balloonOnly verifies that VMResize sends PUT /vm.resize
// with only desired_balloon set and desired_ram/desired_vcpus absent (nil →
// omitted by omitempty), and that the balloon value is in bytes.
func TestClient_VMResize_balloonOnly(t *testing.T) {
	var gotBody []byte
	var gotMethod string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vm.resize", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	})

	sock := unixTestServer(t, mux)
	c := newClient(sock)

	const balloonMiB = 256
	balloonBytes := uint64(balloonMiB * 1024 * 1024)
	if err := c.VMResize(context.Background(), nil, nil, &balloonBytes); err != nil {
		t.Fatalf("VMResize: unexpected error: %v", err)
	}

	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}

	var body map[string]any
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatalf("decode request body: %v; raw: %s", err, gotBody)
	}
	want := float64(balloonMiB * 1024 * 1024)
	if v, _ := body["desired_balloon"].(float64); v != want {
		t.Errorf("desired_balloon = %v, want %v (%d MiB in bytes)", v, want, balloonMiB)
	}
	// nil RAM and vCPU must be absent (omitempty).
	if _, ok := body["desired_ram"]; ok {
		t.Error("desired_ram present in body when nil; omitempty should suppress it")
	}
	if _, ok := body["desired_vcpus"]; ok {
		t.Error("desired_vcpus present in body when nil; omitempty should suppress it")
	}
}

// TestClient_VMResize_allFields verifies that non-nil RAM/vCPU/balloon all
// appear in the request body with the correct byte/count values.
func TestClient_VMResize_allFields(t *testing.T) {
	var gotBody []byte
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vm.resize", func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	})

	sock := unixTestServer(t, mux)
	c := newClient(sock)

	ram := uint64(2 * 1024 * 1024 * 1024) // 2 GiB
	vcpus := uint32(4)
	balloon := uint64(512 * 1024 * 1024) // 512 MiB
	if err := c.VMResize(context.Background(), &ram, &vcpus, &balloon); err != nil {
		t.Fatalf("VMResize: unexpected error: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatalf("decode request body: %v; raw: %s", err, gotBody)
	}
	if v, _ := body["desired_ram"].(float64); v != float64(ram) {
		t.Errorf("desired_ram = %v, want %v", v, float64(ram))
	}
	if v, _ := body["desired_vcpus"].(float64); v != float64(vcpus) {
		t.Errorf("desired_vcpus = %v, want %v", v, float64(vcpus))
	}
	if v, _ := body["desired_balloon"].(float64); v != float64(balloon) {
		t.Errorf("desired_balloon = %v, want %v", v, float64(balloon))
	}
}

// TestClient_VMResize_unexpectedStatus verifies that a non-204 response from
// vm.resize returns a non-nil error.
func TestClient_VMResize_unexpectedStatus(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vm.resize", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	})

	sock := unixTestServer(t, mux)
	c := newClient(sock)

	balloon := uint64(0)
	err := c.VMResize(context.Background(), nil, nil, &balloon)
	if err == nil {
		t.Fatal("VMResize: expected error for 400 response, got nil")
	}
}

// TestResizeBalloon_driver verifies that CHDriver.ResizeBalloon sends the
// correct PUT /vm.resize body to the sandbox's socket. It wires a fake HTTP
// server at d.socketPath(id) — the same pattern used by TestStop_callsVMMShutdown.
func TestResizeBalloon_driver(t *testing.T) {
	dir := testSocketDir(t)
	d := newTestDriver(t, dir)
	id := domain.NewSandboxID()

	var gotBody []byte
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vm.resize", func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	})

	sockPath := d.socketPath(id)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)

	const balloonMiB = 128
	if err := d.ResizeBalloon(context.Background(), id, balloonMiB); err != nil {
		t.Fatalf("ResizeBalloon: unexpected error: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatalf("decode request body: %v; raw: %s", err, gotBody)
	}
	want := float64(balloonMiB * 1024 * 1024)
	if v, _ := body["desired_balloon"].(float64); v != want {
		t.Errorf("desired_balloon = %v, want %v (%d MiB in bytes)", v, want, balloonMiB)
	}
	if _, ok := body["desired_ram"]; ok {
		t.Error("desired_ram must be absent for balloon-only resize")
	}
	if _, ok := body["desired_vcpus"]; ok {
		t.Error("desired_vcpus must be absent for balloon-only resize")
	}
}
