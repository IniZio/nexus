package clientagent

import (
	"context"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	listProcesses = func(_ context.Context) ([]procEntry, error) {
		return nil, nil
	}
	os.Exit(m.Run())
}
