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
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
)

func TestBalloonMemoryResizer_ObserveSample(t *testing.T) {
	const mib = 1024 * 1024
	tests := []struct {
		name        string
		initBalloon uint32
		memTotal    uint64
		memAvail    uint64
		wantStatus  DriftStatus
		wantBalloon uint32
	}{
		{
			name: "no violation when used >= balloon",
			initBalloon: 6144, memTotal: 8192 * mib, memAvail: 512 * mib,
			wantStatus: DriftOK, wantBalloon: 6144,
		},
		{
			name: "suspect on first violation sample",
			initBalloon: 6144, memTotal: 8192 * mib, memAvail: 7000 * mib,
			wantStatus: DriftSuspect, wantBalloon: 6144,
		},
		{
			name: "never raises balloon",
			initBalloon: 100, memTotal: 8192 * mib, memAvail: 512 * mib,
			wantStatus: DriftOK, wantBalloon: 100,
		},
		{
			name: "sub-MiB total skips correction",
			initBalloon: 6144, memTotal: 100, memAvail: 0,
			wantStatus: DriftOK, wantBalloon: 6144,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewBalloonMemoryResizer(nil, domain.NewSandboxID(), 8192, 2048, tc.initBalloon)
			status, newBalloon := r.ObserveSample(tc.memTotal, tc.memAvail)
			if status != tc.wantStatus {
				t.Errorf("status = %v, want %v", status, tc.wantStatus)
			}
			if newBalloon != tc.wantBalloon {
				t.Errorf("newBalloonMiB = %d, want %d", newBalloon, tc.wantBalloon)
			}
		})
	}
}

func TestBalloonMemoryResizer_ObserveSample_Window(t *testing.T) {
	const mib = 1024 * 1024
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("2 samples at 5s no correction", func(t *testing.T) {
		var fakeNow time.Time = base
		r := NewBalloonMemoryResizer(nil, domain.NewSandboxID(), 8192, 2048, 6144)
		r.SetClock(func() time.Time { return fakeNow })
		total, avail := uint64(8192*mib), uint64(7000*mib)
		r.ObserveSample(total, avail)
		fakeNow = base.Add(5 * time.Second)
		st, bal := r.ObserveSample(total, avail)
		if st == DriftCorrected {
			t.Errorf("corrected after only 2 samples / 5s, want no correction")
		}
		if bal != 6144 {
			t.Errorf("balloon = %d, want 6144", bal)
		}
	})

	t.Run("3 samples spanning 15s corrects", func(t *testing.T) {
		var fakeNow time.Time = base
		r := NewBalloonMemoryResizer(nil, domain.NewSandboxID(), 8192, 2048, 6144)
		r.SetClock(func() time.Time { return fakeNow })
		total, avail := uint64(8192*mib), uint64(7000*mib)
		r.ObserveSample(total, avail)
		fakeNow = base.Add(8 * time.Second)
		r.ObserveSample(total, avail)
		fakeNow = base.Add(16 * time.Second)
		st, bal := r.ObserveSample(total, avail)
		if st != DriftCorrected {
			t.Errorf("status = %v, want DriftCorrected", st)
		}
		if bal != 1192 {
			t.Errorf("balloon = %d, want 1192", bal)
		}
		if r.BalloonBytes() != int64(1192)*mib {
			t.Errorf("BalloonBytes not updated: %d", r.BalloonBytes())
		}
	})

	t.Run("reset when used >= tracked in between", func(t *testing.T) {
		var fakeNow time.Time = base
		r := NewBalloonMemoryResizer(nil, domain.NewSandboxID(), 8192, 2048, 6144)
		r.SetClock(func() time.Time { return fakeNow })
		total, avail := uint64(8192*mib), uint64(7000*mib)
		r.ObserveSample(total, avail)
		fakeNow = base.Add(8 * time.Second)
		r.ObserveSample(total, avail)
		fakeNow = base.Add(9 * time.Second)
		r.ObserveSample(8192*mib, 512*mib) // used=7680 MiB > balloon=6144 → resets
		fakeNow = base.Add(25 * time.Second)
		st, _ := r.ObserveSample(total, avail)
		if st == DriftCorrected {
			t.Errorf("corrected after reset, want no correction yet (count reset to 1)")
		}
	})
}

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
