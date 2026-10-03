package cli

import (
	"strings"
	"testing"
)

func TestCheckStaleMCPProcs(t *testing.T) {
	ok := checkStaleMCPProcs(func() []int { return nil })
	if !ok.OK || !ok.Optional {
		t.Fatalf("no stale procs: want OK optional check, got %+v", ok)
	}
	bad := checkStaleMCPProcs(func() []int { return []int{42, 43} })
	if bad.OK || !strings.Contains(bad.Detail, "42") || bad.Remediation == "" {
		t.Fatalf("stale procs: want failing check naming pids, got %+v", bad)
	}
}
