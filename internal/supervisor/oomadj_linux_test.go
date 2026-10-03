//go:build linux

package supervisor

import (
	"errors"
	"testing"
)

func stubOOM(t *testing.T, cur string, werr error) *[]int {
	t.Helper()
	var writes []int
	or, ow := readOOMScoreAdj, writeOOMScoreAdj
	t.Cleanup(func() { readOOMScoreAdj, writeOOMScoreAdj = or, ow })
	readOOMScoreAdj = func(string) ([]byte, error) { return []byte(cur + "\n"), nil }
	writeOOMScoreAdj = func(_ string, v int) error { writes = append(writes, v); return werr }
	return &writes
}

func TestOOMScoreAdjRaisesWhenLower(t *testing.T) {
	w := stubOOM(t, "-900", nil)
	raiseOOMScoreAdj()
	if len(*w) != 1 || (*w)[0] != supervisorOOMScoreAdj {
		t.Fatalf("writes=%v want [%d]", *w, supervisorOOMScoreAdj)
	}
}

func TestOOMScoreAdjNoLowering(t *testing.T) {
	w := stubOOM(t, "1000", nil)
	raiseOOMScoreAdj()
	if len(*w) != 0 {
		t.Fatalf("unexpected writes %v", *w)
	}
}

func TestOOMScoreAdjWriteFailureTolerated(t *testing.T) {
	w := stubOOM(t, "0", errors.New("EACCES"))
	raiseOOMScoreAdj()
	if len(*w) != 1 {
		t.Fatalf("writes=%v", *w)
	}
}
