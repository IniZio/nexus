// Package selfhost provides the netns re-exec sentinel for TestSelfHostE2E.
//
// StartNetnsRuntime re-execs this binary with NEXUS3_NETNS_RUN=1 inside a
// user+network namespace to host the cloud-hypervisor VMM and TAP/bridge
// topology. Without TestMain checking for this sentinel, the re-exec'd binary
// would try to run test functions instead of the netns child work — causing
// "VMM API not ready" failures with Go module cache errors in the child stderr.
//
// This file has no build constraint so it is always compiled, even without
// -tags integration. That is required: the re-exec dispatch must be present
// in any build of the test binary.
package selfhost

import (
	"os"
	"testing"

	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
)

// TestMain is the test binary entry point. It dispatches the netns re-exec
// sentinel (same pattern as ch_netns_test.go and egress_e2e_integration_test.go)
// and otherwise delegates to the test runner.
func TestMain(m *testing.M) {
	if os.Getenv(cloudhypervisor.NetnsRunEnv) == "1" {
		cloudhypervisor.RunNetnsChild()
		os.Exit(0)
	}
	os.Exit(m.Run())
}
