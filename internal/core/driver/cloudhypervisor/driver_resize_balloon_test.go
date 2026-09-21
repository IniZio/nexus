//go:build linux

package cloudhypervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/resize"
)

func setTestMemState(d *CHDriver, id domain.SandboxID, st *vmMemState) {
	d.memMu.Lock()
	d.memState[id] = st
	d.memMu.Unlock()
}

func newBalloonTestState(totalMiB, bootMiB, initBalloonMiB uint32) *vmMemState {
	st := &vmMemState{
		mode:     driver.MemoryModeBalloon,
		bootMiB:  bootMiB,
		totalMiB: totalMiB,
	}
	st.balloon.Store(initBalloonMiB)
	return st
}

func TestObserveSample_Basic(t *testing.T) {
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
			name:        "no violation when used >= balloon",
			initBalloon: 6144, memTotal: 8192 * mib, memAvail: 512 * mib,
			wantStatus: DriftOK, wantBalloon: 6144,
		},
		{
			name:        "suspect on first violation sample",
			initBalloon: 6144, memTotal: 8192 * mib, memAvail: 7000 * mib,
			wantStatus: DriftSuspect, wantBalloon: 6144,
		},
		{
			name:        "never raises balloon",
			initBalloon: 100, memTotal: 8192 * mib, memAvail: 512 * mib,
			wantStatus: DriftOK, wantBalloon: 100,
		},
		{
			name:        "sub-MiB total skips correction",
			initBalloon: 6144, memTotal: 100, memAvail: 0,
			wantStatus: DriftOK, wantBalloon: 6144,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := testSocketDir(t)
			d := newTestDriver(t, dir)
			id := domain.NewSandboxID()
			setTestMemState(d, id, newBalloonTestState(8192, 2048, tc.initBalloon))

			status, newBalloon := d.ObserveSample(id, tc.memTotal, tc.memAvail)
			if status != tc.wantStatus {
				t.Errorf("status = %v, want %v", status, tc.wantStatus)
			}
			if newBalloon != tc.wantBalloon {
				t.Errorf("newBalloonMiB = %d, want %d", newBalloon, tc.wantBalloon)
			}
		})
	}
}

func TestObserveSample_Window(t *testing.T) {
	const mib = 1024 * 1024
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("2 samples at 5s no correction", func(t *testing.T) {
		fakeNow := base
		dir := testSocketDir(t)
		d := newTestDriver(t, dir)
		id := domain.NewSandboxID()
		st := newBalloonTestState(8192, 2048, 6144)
		st.clockFn = func() time.Time { return fakeNow }
		setTestMemState(d, id, st)

		total, avail := uint64(8192*mib), uint64(7000*mib)
		d.ObserveSample(id, total, avail)
		fakeNow = base.Add(5 * time.Second)
		status, bal := d.ObserveSample(id, total, avail)
		if status == DriftCorrected {
			t.Errorf("corrected after only 2 samples / 5s, want no correction")
		}
		if bal != 6144 {
			t.Errorf("balloon = %d, want 6144", bal)
		}
	})

	t.Run("3 samples spanning 15s corrects", func(t *testing.T) {
		fakeNow := base
		dir := testSocketDir(t)
		d := newTestDriver(t, dir)
		id := domain.NewSandboxID()
		st := newBalloonTestState(8192, 2048, 6144)
		st.clockFn = func() time.Time { return fakeNow }
		setTestMemState(d, id, st)

		total, avail := uint64(8192*mib), uint64(7000*mib)
		d.ObserveSample(id, total, avail)
		fakeNow = base.Add(8 * time.Second)
		d.ObserveSample(id, total, avail)
		fakeNow = base.Add(16 * time.Second)
		status, bal := d.ObserveSample(id, total, avail)
		if status != DriftCorrected {
			t.Errorf("status = %v, want DriftCorrected", status)
		}
		if bal != 1192 {
			t.Errorf("balloon = %d, want 1192", bal)
		}
		if d.BalloonBytes(id) != int64(1192)*mib {
			t.Errorf("BalloonBytes not updated: %d", d.BalloonBytes(id))
		}
	})

	t.Run("reset when used >= tracked in between", func(t *testing.T) {
		fakeNow := base
		dir := testSocketDir(t)
		d := newTestDriver(t, dir)
		id := domain.NewSandboxID()
		st := newBalloonTestState(8192, 2048, 6144)
		st.clockFn = func() time.Time { return fakeNow }
		setTestMemState(d, id, st)

		total, avail := uint64(8192*mib), uint64(7000*mib)
		d.ObserveSample(id, total, avail)
		fakeNow = base.Add(8 * time.Second)
		d.ObserveSample(id, total, avail)
		fakeNow = base.Add(9 * time.Second)
		d.ObserveSample(id, 8192*mib, 512*mib)
		fakeNow = base.Add(25 * time.Second)
		status, _ := d.ObserveSample(id, total, avail)
		if status == DriftCorrected {
			t.Errorf("corrected after reset, want no correction yet (count reset to 1)")
		}
	})

	t.Run("3 samples span<15s no correction", func(t *testing.T) {
		fakeNow := base
		dir := testSocketDir(t)
		d := newTestDriver(t, dir)
		id := domain.NewSandboxID()
		st := newBalloonTestState(8192, 2048, 6144)
		st.clockFn = func() time.Time { return fakeNow }
		setTestMemState(d, id, st)

		total, avail := uint64(8192*mib), uint64(7000*mib)
		status, bal := d.ObserveSample(id, total, avail)
		if status != DriftSuspect {
			t.Errorf("first sample: status = %v, want DriftSuspect", status)
		}
		fakeNow = base.Add(4 * time.Second)
		d.ObserveSample(id, total, avail)
		fakeNow = base.Add(8 * time.Second)
		status, bal = d.ObserveSample(id, total, avail)
		if status == DriftCorrected {
			t.Errorf("corrected with span 8s < 15s, want no correction")
		}
		if bal != 6144 {
			t.Errorf("balloon = %d, want 6144", bal)
		}
	})
}

func TestResizeMemory_ResetsDrift(t *testing.T) {
	const mib = 1024 * 1024
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	fakeNow := base

	dir := testSocketDir(t)
	d := newTestDriver(t, dir)
	id := domain.NewSandboxID()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vm.resize", func(w http.ResponseWriter, _ *http.Request) {
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

	st := newBalloonTestState(8192, 2048, 6144)
	st.clockFn = func() time.Time { return fakeNow }
	setTestMemState(d, id, st)

	total, avail := uint64(8192*mib), uint64(7000*mib)
	d.ObserveSample(id, total, avail)
	fakeNow = base.Add(10 * time.Second)
	d.ObserveSample(id, total, avail)

	if _, err := d.ResizeMemory(context.Background(), id, int64(4096*mib)); err != nil {
		t.Fatalf("ResizeMemory: %v", err)
	}

	fakeNow = base.Add(20 * time.Second)
	status, _ := d.ObserveSample(id, total, avail)
	if status == DriftCorrected {
		t.Errorf("DriftCorrected after resize reset, want DriftSuspect")
	}
	if status != DriftSuspect {
		t.Errorf("status = %v after resize reset, want DriftSuspect", status)
	}
}

func TestCurrentMemoryBytes_BalloonMode(t *testing.T) {
	dir := testSocketDir(t)
	d := newTestDriver(t, dir)
	id := domain.NewSandboxID()
	setTestMemState(d, id, newBalloonTestState(8192, 2048, 6144))

	want := int64(2048) * 1024 * 1024
	if got := d.CurrentMemoryBytes(id); got != want {
		t.Errorf("CurrentMemoryBytes = %d, want %d", got, want)
	}
}

func TestResizeMemory_BalloonDispatch(t *testing.T) {
	tests := []struct {
		name           string
		totalMiB       uint32
		bootMiB        uint32
		initBalloon    uint32
		targetBytes    int64
		wantEffMiB     uint32
		wantBalloonMiB uint32
	}{
		{
			name:     "grow to 4 GiB",
			totalMiB: 8192, bootMiB: 2048, initBalloon: 6144,
			targetBytes:    int64(4096) * 1024 * 1024,
			wantEffMiB:     4096,
			wantBalloonMiB: 4096,
		},
		{
			name:     "grow to ceiling",
			totalMiB: 8192, bootMiB: 2048, initBalloon: 6144,
			targetBytes:    int64(8192) * 1024 * 1024,
			wantEffMiB:     8192,
			wantBalloonMiB: 0,
		},
		{
			name:     "clamp below minimum (boot)",
			totalMiB: 8192, bootMiB: 2048, initBalloon: 0,
			targetBytes:    int64(512) * 1024 * 1024,
			wantEffMiB:     2048,
			wantBalloonMiB: 6144,
		},
		{
			name:     "clamp above ceiling",
			totalMiB: 8192, bootMiB: 2048, initBalloon: 0,
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

			setTestMemState(d, id, newBalloonTestState(tc.totalMiB, tc.bootMiB, tc.initBalloon))
			got, err := d.ResizeMemory(context.Background(), id, tc.targetBytes)
			if err != nil {
				t.Fatalf("ResizeMemory: %v", err)
			}
			wantEff := int64(tc.wantEffMiB) * 1024 * 1024
			if got != wantEff {
				t.Errorf("returned %d, want %d", got, wantEff)
			}
			if cur := d.CurrentMemoryBytes(id); cur != wantEff {
				t.Errorf("CurrentMemoryBytes = %d, want %d", cur, wantEff)
			}
			wantBalloon := float64(tc.wantBalloonMiB) * 1024 * 1024
			if gotBalloonBytes != wantBalloon {
				t.Errorf("desired_balloon = %v, want %v", gotBalloonBytes, wantBalloon)
			}
		})
	}
}

func TestResizeMemory_FlatModeErrors(t *testing.T) {
	dir := testSocketDir(t)
	d := newTestDriver(t, dir)
	id := domain.NewSandboxID()
	st := &vmMemState{mode: driver.MemoryModeFlat, bootMiB: 512, totalMiB: 512}
	setTestMemState(d, id, st)

	_, err := d.ResizeMemory(context.Background(), id, int64(1024)*1024*1024)
	if err == nil {
		t.Error("expected error for flat mode resize, got nil")
	}
}

func TestAdoptMemState_Detection(t *testing.T) {
	tests := []struct {
		name         string
		infoJSON     string
		wantMode     driver.MemoryMode
		wantBalloon  uint32
		wantBootMiB  uint32
		wantTotalMiB uint32
	}{
		{
			name:         "balloon mode",
			infoJSON:     `{"state":"Running","config":{"memory":{"size":` + itoa(8192*1024*1024) + `},"balloon":{"size":` + itoa(6144*1024*1024) + `}}}`,
			wantMode:     driver.MemoryModeBalloon,
			wantBalloon:  6144,
			wantBootMiB:  2048,
			wantTotalMiB: 8192,
		},
		{
			name:         "balloon fully deflated uses hint",
			infoJSON:     `{"state":"Running","config":{"memory":{"size":` + itoa(4096*1024*1024) + `},"balloon":{"size":0}}}`,
			wantMode:     driver.MemoryModeBalloon,
			wantBalloon:  0,
			wantBootMiB:  512,
			wantTotalMiB: 4096,
		},
		{
			name:         "virtiomem legacy",
			infoJSON:     `{"state":"Running","config":{"memory":{"size":` + itoa(2048*1024*1024) + `,"hotplug_size":` + itoa(6144*1024*1024) + `}}}`,
			wantMode:     driver.MemoryModeVirtioMem,
			wantBalloon:  0,
			wantBootMiB:  2048,
			wantTotalMiB: 8192,
		},
		{
			name:         "flat",
			infoJSON:     `{"state":"Running","config":{"memory":{"size":` + itoa(512*1024*1024) + `}}}`,
			wantMode:     driver.MemoryModeFlat,
			wantBalloon:  0,
			wantBootMiB:  512,
			wantTotalMiB: 512,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := testSocketDir(t)
			d := newTestDriver(t, dir)
			id := domain.NewSandboxID()

			mux := http.NewServeMux()
			mux.HandleFunc("/api/v1/vm.info", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tc.infoJSON))
			})
			ln, err := net.Listen("unix", d.socketPath(id))
			if err != nil {
				t.Fatalf("listen unix: %v", err)
			}
			srv := httptest.NewUnstartedServer(mux)
			srv.Listener = ln
			srv.Start()
			t.Cleanup(srv.Close)

			st, err := d.getOrLoadMemState(context.Background(), id, 512)
			if err != nil {
				t.Fatalf("getOrLoadMemState: %v", err)
			}
			if st.mode != tc.wantMode {
				t.Errorf("mode = %v, want %v", st.mode, tc.wantMode)
			}
			if got := st.balloon.Load(); got != tc.wantBalloon {
				t.Errorf("balloon = %d, want %d", got, tc.wantBalloon)
			}
			if st.bootMiB != tc.wantBootMiB {
				t.Errorf("bootMiB = %d, want %d", st.bootMiB, tc.wantBootMiB)
			}
			if st.totalMiB != tc.wantTotalMiB {
				t.Errorf("totalMiB = %d, want %d", st.totalMiB, tc.wantTotalMiB)
			}
		})
	}
}

func TestNewSandboxResizer_AdoptPath(t *testing.T) {
	const mib = 1024 * 1024
	tests := []struct {
		name         string
		vmInfoJSON   string
		bootMemBytes int64
		wantMode     driver.MemoryMode
		wantBalloonB int64
		resizeTarget int64
		wantReqField string
		wantResizeB  int64
	}{
		{
			name:         "balloon",
			vmInfoJSON:   `{"state":"Running","config":{"memory":{"size":` + itoa(8192*mib) + `},"balloon":{"size":` + itoa(6144*mib) + `}}}`,
			bootMemBytes: 2048 * mib,
			wantMode:     driver.MemoryModeBalloon,
			wantBalloonB: 6144 * mib,
			resizeTarget: 4096 * mib,
			wantReqField: "desired_balloon",
			wantResizeB:  4096 * mib,
		},
		{
			name:         "virtiomem",
			vmInfoJSON:   `{"state":"Running","config":{"memory":{"size":` + itoa(2048*mib) + `,"hotplug_size":` + itoa(6144*mib) + `}}}`,
			bootMemBytes: 2048 * mib,
			wantMode:     driver.MemoryModeVirtioMem,
			wantBalloonB: 0,
			resizeTarget: 4096 * mib,
			wantReqField: "desired_ram",
			wantResizeB:  4096 * mib,
		},
		{
			name:         "flat",
			vmInfoJSON:   `{"state":"Running","config":{"memory":{"size":` + itoa(512*mib) + `}}}`,
			bootMemBytes: 512 * mib,
			wantMode:     driver.MemoryModeFlat,
			wantBalloonB: 0,
			resizeTarget: 1024 * mib,
			wantReqField: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := testSocketDir(t)
			d := newTestDriver(t, dir)
			id := domain.NewSandboxID()

			var gotResizeBody map[string]any
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v1/vm.info", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tc.vmInfoJSON))
			})
			mux.HandleFunc("/api/v1/vm.resize", func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(body, &gotResizeBody)
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

			bounds := resize.Bounds{MemMinBytes: 0, MemMaxBytes: int64(8192) * mib}
			resizer := NewSandboxResizer(d, id, bounds, tc.bootMemBytes, 1)

			if got := d.MemoryMode(id); got != tc.wantMode {
				t.Errorf("MemoryMode = %v, want %v", got, tc.wantMode)
			}
			if got := d.BalloonBytes(id); got != tc.wantBalloonB {
				t.Errorf("BalloonBytes = %d, want %d", got, tc.wantBalloonB)
			}

			got, err := resizer.ResizeMemory(context.Background(), tc.resizeTarget)
			if tc.wantReqField == "" {
				if err == nil {
					t.Error("expected error for flat mode resize, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("ResizeMemory: %v", err)
			}
			if got != tc.wantResizeB {
				t.Errorf("ResizeMemory returned %d, want %d", got, tc.wantResizeB)
			}
			if cur := resizer.CurrentMemoryBytes(); cur != tc.wantResizeB {
				t.Errorf("resizer.CurrentMemoryBytes() = %d, want %d", cur, tc.wantResizeB)
			}
			if _, ok := gotResizeBody[tc.wantReqField]; !ok {
				t.Errorf("vm.resize body missing %q; got %v", tc.wantReqField, gotResizeBody)
			}
		})
	}
}

func TestMemoryMode_LazyLoad(t *testing.T) {
	const mib = 1024 * 1024
	dir := testSocketDir(t)
	d := newTestDriver(t, dir)
	id := domain.NewSandboxID()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vm.info", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"state":"Running","config":{"memory":{"size":` + itoa(8192*mib) + `},"balloon":{"size":` + itoa(6144*mib) + `}}}`))
	})
	ln, err := net.Listen("unix", d.socketPath(id))
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)

	if got := d.MemoryMode(id); got != driver.MemoryModeBalloon {
		t.Errorf("MemoryMode (lazy) = %v, want MemoryModeBalloon", got)
	}
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}
