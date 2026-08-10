package recovery_test

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if proc1comm, err := os.ReadFile("/proc/1/comm"); err == nil {
		if strings.TrimSpace(string(proc1comm)) == "nexus3-agent" {
			fmt.Fprintln(os.Stderr, "recovery: skipping tests — running inside nexus3 guest VM (host-side package)")
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}
