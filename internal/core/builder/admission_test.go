package builder

import (
	"errors"
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
