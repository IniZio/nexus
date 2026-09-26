package backendtest_test

import (
	"testing"

	"github.com/IniZio/nexus/internal/controller"
	"github.com/IniZio/nexus/internal/controller/backendtest"
)

func TestContractsAgainstFakes(t *testing.T) {
	backendtest.RunBackendContract(t, func(t *testing.T) controller.AgentBackend {
		t.Helper()
		return backendtest.New()
	})
}
