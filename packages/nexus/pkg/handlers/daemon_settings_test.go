package handlers

import (
	"context"
	"testing"

	"github.com/inizio/nexus/packages/nexus/pkg/store"
)

func TestHandleDaemonSettingsGetDefaults(t *testing.T) {
	result, rpcErr := HandleDaemonSettingsGet(context.Background(), DaemonSettingsGetParams{}, nil)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if result.SandboxResources.DefaultMemoryMiB != sandboxDefaultMemoryMiB {
		t.Fatalf("expected default memory %d, got %d", sandboxDefaultMemoryMiB, result.SandboxResources.DefaultMemoryMiB)
	}
	if result.SandboxResources.DefaultVCPUs != sandboxDefaultVCPUs {
		t.Fatalf("expected default vcpus %d, got %d", sandboxDefaultVCPUs, result.SandboxResources.DefaultVCPUs)
	}
	if result.SandboxResources.MaxMemoryMiB != sandboxMaxMemoryMiB {
		t.Fatalf("expected max memory %d, got %d", sandboxMaxMemoryMiB, result.SandboxResources.MaxMemoryMiB)
	}
	if result.SandboxResources.MaxVCPUs != sandboxMaxVCPUs {
		t.Fatalf("expected max vcpus %d, got %d", sandboxMaxVCPUs, result.SandboxResources.MaxVCPUs)
	}
}

func TestHandleDaemonSettingsUpdatePersists(t *testing.T) {
	repo := &sandboxSettingsRepoStub{}
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
	repo := &sandboxSettingsRepoStub{ok: true, row: store.SandboxResourceSettingsRow{
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
