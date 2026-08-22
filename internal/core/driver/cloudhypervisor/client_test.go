package cloudhypervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/driver"
)

// unixTestServer starts an HTTP server on a Unix socket in a short temp dir
// (bypassing $TMPDIR) and returns the socket path. This avoids the Linux
// sun_path limit regardless of how long $TMPDIR is.
func unixTestServer(t *testing.T, handler http.Handler) (socketPath string) {
	t.Helper()
	socketPath = filepath.Join(testSocketDir(t), "test.sock")

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}

	srv := httptest.NewUnstartedServer(handler)
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)

	return socketPath
}

// TestClient_Ping verifies that Ping returns nil on a 200 OK response.
func TestClient_Ping(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vmm.ping", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"version":"52.0"}`)
	})

	sock := unixTestServer(t, mux)
	c := newClient(sock)

	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: unexpected error: %v", err)
	}
}

// TestClient_Ping_absent verifies that Ping on a non-existent socket returns
// an error for which isAbsent is true.
func TestClient_Ping_absent(t *testing.T) {
	c := newClient(filepath.Join(testSocketDir(t), "no-such.sock"))
	err := c.Ping(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !isAbsent(err) {
		t.Fatalf("expected isAbsent(err)=true, got false; err=%v", err)
	}
}

// TestClient_VMInfo_stateMapping verifies every documented CH v52 state string.
func TestClient_VMInfo_stateMapping(t *testing.T) {
	tests := []struct {
		chState   string
		wantState driver.RunState
		wantErr   bool
	}{
		{"Running", driver.Running, false},
		{"Paused", driver.Paused, false},
		// Created and Shutdown must produce Unknown + non-nil error (see
		// mapCHState comment for the full rationale — returning Absent or
		// Running for these states would be unsafe).
		{"Created", driver.Unknown, true},
		{"Shutdown", driver.Unknown, true},
	}

	for _, tt := range tests {
		t.Run(tt.chState, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v1/vm.info", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]any{"state": tt.chState, "config": map[string]any{}})
			})

			sock := unixTestServer(t, mux)
			c := newClient(sock)

			state, err := c.VMInfo(context.Background())
			if state != tt.wantState {
				t.Errorf("state = %v, want %v", state, tt.wantState)
			}
			if tt.wantErr && err == nil {
				t.Error("expected non-nil error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// TestClient_VMInfo_unrecognisedState verifies that an unrecognised state
// string produces Unknown + a non-nil error (fail loudly, not silently).
func TestClient_VMInfo_unrecognisedState(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vm.info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{"state": "Hibernated", "config": map[string]any{}})
	})

	sock := unixTestServer(t, mux)
	c := newClient(sock)

	state, err := c.VMInfo(context.Background())
	if state != driver.Unknown {
		t.Errorf("state = %v, want Unknown", state)
	}
	if err == nil {
		t.Fatal("expected non-nil error for unrecognised state, got nil")
	}
}

// TestClient_VMInfo_absent verifies that a connection to a non-existent socket
// returns an error for which isAbsent is true (the caller maps this to Absent).
func TestClient_VMInfo_absent(t *testing.T) {
	c := newClient(filepath.Join(testSocketDir(t), "no-such.sock"))
	state, err := c.VMInfo(context.Background())
	if state != driver.Unknown {
		t.Errorf("state = %v, want Unknown (caller will convert via isAbsent)", state)
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !isAbsent(err) {
		t.Fatalf("expected isAbsent(err)=true; err=%v", err)
	}
}

// TestClient_VMInfo_404 verifies that a 404 from vm.info returns Unknown+error.
// Cloud-hypervisor v52.0 never returns 404 for vm.info (it returns 500 "VM is
// not created"), so a 404 is unrecognised — the state is undetermined, which is
// driver.Unknown. This is distinct from the 500 "VM is not created" case which
// is a confirmed "no VM", mapped to Absent.
func TestClient_VMInfo_404(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vm.info", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})

	sock := unixTestServer(t, mux)
	c := newClient(sock)

	state, err := c.VMInfo(context.Background())
	if state != driver.Unknown {
		t.Errorf("state = %v, want Unknown (live VMM, non-200 response)", state)
	}
	if err == nil {
		t.Error("expected non-nil error for 404, got nil")
	}
}

// TestClient_VMInfo_500_noVM verifies the real cloud-hypervisor v52.0 path:
// vm.info returns 500 "VM is not created" when no VM has been configured.
// This is an unambiguous observation — no VM exists — so it maps to
// (driver.Absent, nil). The caller (Observe) can safely return Absent
// because spawnVMM now pre-flights and refuses to spawn onto an already-live
// VMM socket (the concern that motivated the old Unknown mapping).
func TestClient_VMInfo_500_noVM(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vm.info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `["Error from API","The VM info is not available","VM is not created"]`)
	})

	sock := unixTestServer(t, mux)
	c := newClient(sock)

	state, err := c.VMInfo(context.Background())
	if state != driver.Absent {
		t.Errorf("state = %v, want Absent for 500 'VM is not created'", state)
	}
	if err != nil {
		t.Errorf("unexpected error for 500 'VM is not created': %v", err)
	}
}

// TestClient_VMInfo_500_genericError verifies that a 500 with any body maps to
// Unknown + non-nil error — all non-200 responses follow the same path.
func TestClient_VMInfo_500_genericError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vm.info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `["Error from API","Something unexpected happened","disk is full"]`)
	})

	sock := unixTestServer(t, mux)
	c := newClient(sock)

	state, err := c.VMInfo(context.Background())
	if state != driver.Unknown {
		t.Errorf("state = %v, want Unknown for 500", state)
	}
	if err == nil {
		t.Error("expected non-nil error for 500, got nil")
	}
}

// TestClient_VMShutdown_500_noVM verifies that vm.shutdown returning 500 "VM is
// not running" is treated as success (nothing to shut down).
func TestClient_VMShutdown_500_noVM(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vm.shutdown", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `["Error from API","The VM could not shutdown","VM is not running"]`)
	})

	sock := unixTestServer(t, mux)
	c := newClient(sock)

	if err := c.VMShutdown(context.Background()); err != nil {
		t.Errorf("VMShutdown: expected nil for 'VM is not running', got %v", err)
	}
}

// TestClient_VMMShutdown_200 verifies that vmm.shutdown returning 200 (as the
// real cloud-hypervisor v52.0 does) is treated as success.
func TestClient_VMMShutdown_200(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vmm.shutdown", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK) // real CH returns 200
	})

	sock := unixTestServer(t, mux)
	c := newClient(sock)

	if err := c.VMMShutdown(context.Background()); err != nil {
		t.Errorf("VMMShutdown: unexpected error: %v", err)
	}
}

// TestClient_VMInfo_contextTimeout verifies the critical invariant:
// a server that accepts the connection but never responds must produce
// Unknown + a deadline error, NOT Absent. Conflating the two is how a live
// VM gets destroyed.
func TestClient_VMInfo_contextTimeout(t *testing.T) {
	// Accept the connection but never write a response (hung server).
	hungHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until the test is over.
		<-r.Context().Done()
	})

	sock := unixTestServer(t, hungHandler)
	c := newClient(sock)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	state, err := c.VMInfo(ctx)
	if state != driver.Unknown {
		t.Errorf("state = %v, want Unknown", state)
	}
	if err == nil {
		t.Fatal("expected non-nil error for hung server, got nil")
	}
	if isAbsent(err) {
		t.Fatalf("isAbsent(err) must be false for a hung server: a live VM must not appear absent; err=%v", err)
	}
}

// TestClient_VMInfo_malformedJSON verifies that malformed JSON produces
// Unknown + a non-nil error.
func TestClient_VMInfo_malformedJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vm.info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{not valid json`)
	})

	sock := unixTestServer(t, mux)
	c := newClient(sock)

	state, err := c.VMInfo(context.Background())
	if state != driver.Unknown {
		t.Errorf("state = %v, want Unknown", state)
	}
	if err == nil {
		t.Fatal("expected non-nil error for malformed JSON, got nil")
	}
}

// TestClient_isAbsent_notTriggeredByTimeout is a guard: context.DeadlineExceeded
// must not satisfy isAbsent.
func TestClient_isAbsent_notTriggeredByTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()

	if isAbsent(ctx.Err()) {
		t.Fatal("isAbsent(context.DeadlineExceeded) must be false")
	}
}

// TestVmMemoryConfig_SharedField verifies that vmMemoryConfig serialises
// "shared":true when Shared=true, and omits the key entirely when Shared=false
// (omitempty behaviour — CH defaults to false so the key need not be present).
func TestVmMemoryConfig_SharedField(t *testing.T) {
	unmarshal := func(t *testing.T, v any) map[string]any {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("json.Unmarshal: %v", err)
		}
		return m
	}

	// Shared=true must appear in the JSON body sent to CH.
	m := unmarshal(t, vmMemoryConfig{SizeBytes: 512 * 1024 * 1024, Shared: true})
	if v, _ := m["shared"].(bool); !v {
		t.Errorf("shared = %v, want true (CH requires shared=true for vhost-user/virtiofs)", m["shared"])
	}

	// Shared=false (zero value) must be omitted via omitempty — CH defaults to false.
	m2 := unmarshal(t, vmMemoryConfig{SizeBytes: 512 * 1024 * 1024})
	if _, present := m2["shared"]; present {
		b, _ := json.Marshal(vmMemoryConfig{SizeBytes: 512 * 1024 * 1024})
		t.Errorf("shared present when false (omitempty should suppress it); raw=%s", b)
	}
}
