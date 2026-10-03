//go:build linux

package agent

import "testing"

func TestRoMountedPaths(t *testing.T) {
	mi := "28 21 0:25 / /r/.git rw,relatime - virtiofs nxfs0 rw\n" +
		"29 28 0:25 /config /r/.git/config ro,relatime - virtiofs nxfs0 rw\n"
	got := roMountedPaths(mi)
	if !got["/r/.git/config"] || got["/r/.git"] {
		t.Fatalf("got %v", got)
	}
}
