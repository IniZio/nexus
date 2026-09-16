//go:build linux

package main

import (
	"path/filepath"
	"testing"
)

func setVmstatPath(t *testing.T, path string) {
	t.Helper()
	orig := sampleVmstatPath
	t.Cleanup(func() { sampleVmstatPath = orig })
	sampleVmstatPath = path
}

func TestReadVmstatPswpin(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		content string
		missing bool
		want    uint64
	}{
		{
			name: "present",
			content: "nr_free_pages 12345\n" +
				"pswpout 99\n" +
				"pswpin 4242\n" +
				"pgpgin 1\n",
			want: 4242,
		},
		{name: "zero", content: "pswpin 0\npswpout 0\n", want: 0},
		{name: "missing_key", content: "nr_free_pages 12345\npswpout 7\n", want: 0},
		{name: "unparsable", content: "pswpin abc\n", want: 0},
		{name: "missing_file", missing: true, want: 0},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "vmstat")
			if !tc.missing {
				writeTestFile(t, path, tc.content)
			}
			if got := readVmstatPswpin(path); got != tc.want {
				t.Errorf("readVmstatPswpin = %d, want %d", got, tc.want)
			}
		})
	}
}

// collectSample must carry pswpin into Sample.SwapInPages, and a guest kernel
// without /proc/vmstat must still yield a sample (SwapInPages = 0, no error).
func TestCollectSampleSwapInPages(t *testing.T) {
	dir := t.TempDir()
	mi := filepath.Join(dir, "meminfo")
	writeTestFile(t, mi, "MemTotal: 8388608 kB\nMemFree: 4194304 kB\nMemAvailable: 5242880 kB\n")
	setMeminfoPath(t, mi)
	setPSIPaths(t, filepath.Join(dir, "noexist"), filepath.Join(dir, "noexist"))
	setStatfsFunc(t, noopStatfs)
	setCPUSysPath(t, filepath.Join(dir, "no-cpu"))

	vm := filepath.Join(dir, "vmstat")
	writeTestFile(t, vm, "pswpin 777\npswpout 5\n")
	setVmstatPath(t, vm)

	s, err := collectSample(nil)
	if err != nil {
		t.Fatalf("collectSample: %v", err)
	}
	if s.SwapInPages != 777 {
		t.Fatalf("SwapInPages = %d, want 777", s.SwapInPages)
	}

	setVmstatPath(t, filepath.Join(dir, "no-vmstat"))
	s, err = collectSample(nil)
	if err != nil {
		t.Fatalf("collectSample without vmstat: %v", err)
	}
	if s.SwapInPages != 0 {
		t.Fatalf("SwapInPages without vmstat = %d, want 0", s.SwapInPages)
	}
}
