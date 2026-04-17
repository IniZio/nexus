package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/store"
)

type daemonSettingsRepoStub struct {
	row store.SandboxResourceSettingsRow
	ok  bool
	err error
}

func (s *daemonSettingsRepoStub) GetSandboxResourceSettings() (store.SandboxResourceSettingsRow, bool, error) {
	return s.row, s.ok, s.err
}

func (s *daemonSettingsRepoStub) UpsertSandboxResourceSettings(row store.SandboxResourceSettingsRow) error {
	s.row = row
	s.ok = true
	s.row.UpdatedAt = time.Now().UTC()
	return nil
}

func TestHandleDaemonSettingsGetDefaults(t *testing.T) {
	result, rpcErr := HandleDaemonSettingsGet(context.Background(), DaemonSettingsGetParams{}, nil)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	// Defaults come from workspace.SandboxResourcePolicyFromRepository(nil)
	if result.SandboxResources.DefaultMemoryMiB != 1024 {
		t.Fatalf("expected default memory 1024, got %d", result.SandboxResources.DefaultMemoryMiB)
	}
	if result.SandboxResources.DefaultVCPUs != 1 {
		t.Fatalf("expected default vcpus 1, got %d", result.SandboxResources.DefaultVCPUs)
	}
	if result.SandboxResources.MaxMemoryMiB != 4096 {
		t.Fatalf("expected max memory 4096, got %d", result.SandboxResources.MaxMemoryMiB)
	}
	if result.SandboxResources.MaxVCPUs != 4 {
		t.Fatalf("expected max vcpus 4, got %d", result.SandboxResources.MaxVCPUs)
	}
}

func TestHandleDaemonSettingsUpdatePersists(t *testing.T) {
	repo := &daemonSettingsRepoStub{}
	req := DaemonSettingsUpdateParams{
		SandboxResources: SandboxResourceSettings{
			DefaultMemoryMiB: 1536,
			DefaultVCPUs:     2,
			MaxMemoryMiB:     4096,
			MaxVCPUs:         4,
		},
	}
	result, rpcErr := HandleDaemonSettingsUpdate(context.Background(), req, repo)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if result.SandboxResources.DefaultMemoryMiB != 1536 {
		t.Fatalf("expected persisted default memory 1536, got %d", result.SandboxResources.DefaultMemoryMiB)
	}
	row, ok, err := repo.GetSandboxResourceSettings()
	if err != nil || !ok {
		t.Fatalf("expected persisted row, ok=%v err=%v", ok, err)
	}
	if row.MaxVCPUs != 4 {
		t.Fatalf("expected max vcpus 4, got %d", row.MaxVCPUs)
	}
}

func TestHandleDaemonSettingsUpdateRejectsInvalid(t *testing.T) {
	repo := &daemonSettingsRepoStub{ok: true, row: store.SandboxResourceSettingsRow{
		DefaultMemoryMiB: 1024, DefaultVCPUs: 1, MaxMemoryMiB: 4096, MaxVCPUs: 4,
	}}
	_, rpcErr := HandleDaemonSettingsUpdate(context.Background(), DaemonSettingsUpdateParams{
		SandboxResources: SandboxResourceSettings{
			DefaultMemoryMiB: 8192,
			DefaultVCPUs:     8,
			MaxMemoryMiB:     4096,
			MaxVCPUs:         4,
		},
	}, repo)
	if rpcErr == nil {
		t.Fatal("expected rpc error for invalid daemon settings update")
	}
}
