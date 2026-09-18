//go:build linux

package cloudhypervisor

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
)

func TestBalloonMemoryResizer_CurrentMemoryBytes(t *testing.T) {
	r := NewBalloonMemoryResizer(nil, domain.NewSandboxID(), 8192, 2048, 6144)
	want := int64(2048) * 1024 * 1024
	if got := r.CurrentMemoryBytes(); got != want {
		t.Errorf("CurrentMemoryBytes = %d, want %d", got, want)
	}
}

func TestBalloonMemoryResizer_ResizeMemory(t *testing.T) {
	tests := []struct {
		name           string
		totalMiB       uint32
		minMiB         uint32
		initBalloon    uint32
		targetBytes    int64
		wantEffMiB     uint32
		wantBalloonMiB uint32
	}{
		{
			name:     "grow to 4 GiB",
			totalMiB: 8192, minMiB: 2048, initBalloon: 6144,
			targetBytes:    int64(4096) * 1024 * 1024,
			wantEffMiB:     4096,
			wantBalloonMiB: 4096,
		},
		{
			name:     "grow to ceiling",
			totalMiB: 8192, minMiB: 2048, initBalloon: 6144,
			targetBytes:    int64(8192) * 1024 * 1024,
			wantEffMiB:     8192,
			wantBalloonMiB: 0,
		},
		{
			name:     "clamp below minimum",
			totalMiB: 8192, minMiB: 2048, initBalloon: 0,
			targetBytes:    int64(512) * 1024 * 1024,
			wantEffMiB:     2048,
			wantBalloonMiB: 6144,
		},
		{
			name:     "clamp above ceiling",
			totalMiB: 8192, minMiB: 2048, initBalloon: 0,
			targetBytes:    int64(16384) * 1024 * 1024,
			wantEffMiB:     8192,
			wantBalloonMiB: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := testSocketDir(t)
			d := newTestDriver(t, dir)
			id := domain.NewSandboxID()

			var gotBalloonBytes float64
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v1/vm.resize", func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				var m map[string]any
				_ = json.Unmarshal(body, &m)
				gotBalloonBytes, _ = m["desired_balloon"].(float64)
				w.WriteHeader(http.StatusNoContent)
			})
			ln, err := net.Listen("unix", d.socketPath(id))
			if err != nil {
				t.Fatalf("listen unix: %v", err)
			}
			srv := httptest.NewUnstartedServer(mux)
			srv.Listener = ln
			srv.Start()
			t.Cleanup(srv.Close)

			r := NewBalloonMemoryResizer(d, id, tc.totalMiB, tc.minMiB, tc.initBalloon)
			got, err := r.ResizeMemory(context.Background(), tc.targetBytes)
			if err != nil {
				t.Fatalf("ResizeMemory: %v", err)
			}
			wantEff := int64(tc.wantEffMiB) * 1024 * 1024
			if got != wantEff {
				t.Errorf("returned %d, want %d", got, wantEff)
			}
			if cur := r.CurrentMemoryBytes(); cur != wantEff {
				t.Errorf("CurrentMemoryBytes = %d, want %d", cur, wantEff)
			}
			wantBalloon := float64(tc.wantBalloonMiB) * 1024 * 1024
			if gotBalloonBytes != wantBalloon {
				t.Errorf("desired_balloon = %v, want %v", gotBalloonBytes, wantBalloon)
			}
		})
	}
}
