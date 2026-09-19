package builder

import (
	"errors"
	"os"
	"testing"
)

func TestAdmitBuilderBoot(t *testing.T) {
	const gib = int64(1024 * 1024 * 1024)
	const bootMiB = uint32(2048)
	const bootBytes = int64(bootMiB) * 1024 * 1024
	total := int64(30) * gib
	floor := admitFloor(total)

	tests := []struct {
		name      string
		avail     int64
		total     int64
		readerErr error
		wantErr   bool
	}{
		{
			name:    "admit: plenty of headroom",
			avail:   bootBytes + floor + gib,
			total:   total,
			wantErr: false,
		},
		{
			name:    "admit: exact boundary",
			avail:   bootBytes + floor,
			total:   total,
			wantErr: false,
		},
		{
			name:    "refuse: one byte short",
			avail:   bootBytes + floor - 1,
			total:   total,
			wantErr: true,
		},
		{
			name:    "refuse: no headroom at all",
			avail:   0,
			total:   total,
			wantErr: true,
		},
		{
			name:      "fail-open: reader error allows boot",
			readerErr: errors.New("io error"),
			wantErr:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reader := func() (int64, int64, error) {
				return tc.avail, tc.total, tc.readerErr
			}
			err := AdmitBuilderBoot(bootMiB, reader)
			if (err != nil) != tc.wantErr {
				t.Errorf("AdmitBuilderBoot() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestGuestMemCeiling(t *testing.T) {
	// Parsed from the guest kernel cmdline the outer nexus writes; absent or
	// malformed → not a nexus guest → plain host semantics.
	cmd := []byte("root=/dev/vda rw init=/sbin/nexus-agent --workspace-mount=nxfs0:/workspace:virtiofs:false:false:false --mem-ceiling=4294967296 --sandbox-handle=x")
	if c, ok := GuestMemCeiling(func() ([]byte, error) { return cmd, nil }); !ok || c != 4294967296 {
		t.Fatalf("ceiling = %d, %v; want 4294967296, true", c, ok)
	}
	if _, ok := GuestMemCeiling(func() ([]byte, error) { return []byte("root=/dev/sda1 ro quiet"), nil }); ok {
		t.Fatal("host cmdline must not report a ceiling")
	}
	if _, ok := GuestMemCeiling(func() ([]byte, error) { return []byte("--mem-ceiling=nope"), nil }); ok {
		t.Fatal("malformed ceiling must not be trusted")
	}
	if _, ok := GuestMemCeiling(func() ([]byte, error) { return nil, os.ErrNotExist }); ok {
		t.Fatal("unreadable cmdline must not report a ceiling")
	}
}

func TestAdmitBuilderBoot_elasticGuestAdmitsAgainstCeiling(t *testing.T) {
	// The live refusal: 4036 MiB balloon, 1160 MiB available, 8 GiB ceiling.
	// Against the instant the builder (2048 boot + floor) is refused; against
	// the ceiling (8192 - 2876 used = 5316 avail) it is admitted, and the
	// outer governor grows the guest under the resulting PSI pressure.
	const mib = 1024 * 1024
	instant := func() (int64, int64, error) { return 1160 * mib, 4036 * mib, nil }
	if err := AdmitBuilderBoot(2048, instant); err == nil {
		t.Fatal("sanity: instantaneous meminfo must refuse the builder in this scenario")
	}
	elastic := func() (int64, int64, error) {
		avail, total, _ := instant()
		ceiling := int64(8192 * mib)
		return ceiling - (total - avail), ceiling, nil
	}
	if err := AdmitBuilderBoot(2048, elastic); err != nil {
		t.Fatalf("ceiling-aware admission refused: %v", err)
	}
	// A ceiling the governor can never exceed still bounds admission.
	tiny := func() (int64, int64, error) { return 500 * mib, 4096 * mib, nil }
	if err := AdmitBuilderBoot(2048, tiny); err == nil {
		t.Fatal("builder larger than the whole ceiling must still be refused")
	}
}
