package chattest_test

import (
	"testing"

	"github.com/IniZio/nexus/internal/controller"
	"github.com/IniZio/nexus/internal/controller/chattest"
)

func TestContractsAgainstFakes(t *testing.T) {
	chattest.RunAdapterContract(t, func(t *testing.T) (controller.ChatAdapter, chattest.Driver) {
		f := chattest.New()
		return f, f
	})
}
