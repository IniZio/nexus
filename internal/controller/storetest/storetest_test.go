package storetest_test

import (
	"testing"

	"github.com/IniZio/nexus/internal/controller"
	"github.com/IniZio/nexus/internal/controller/storetest"
)

func TestContractsAgainstFakes(t *testing.T) {
	storetest.RunTaskStoreContract(t, func(t *testing.T) controller.TaskStore {
		return storetest.New()
	})
}
