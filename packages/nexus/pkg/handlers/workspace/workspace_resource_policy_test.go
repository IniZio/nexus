package workspace

import (
	"testing"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/store"
)

type sandboxSettingsRepoStub struct {
	row store.SandboxResourceSettingsRow
	ok  bool
	err error
}

func (s *sandboxSettingsRepoStub) GetSandboxResourceSettings() (store.SandboxResourceSettingsRow, bool, error) {
	return s.row, s.ok, s.err
}

func (s *sandboxSettingsRepoStub) UpsertSandboxResourceSettings(row store.SandboxResourceSettingsRow) error {
	s.row = row
	s.ok = true
	s.row.UpdatedAt = time.Now().UTC()
	return nil
}

func TestApplySandboxResourcePolicyDefaults(t *testing.T) {
	got := applySandboxResourcePolicy(map[string]string{"host_cli_sync": "true"}, nil)
	if got["mem_mib"] != "1024" {
		t.Fatalf("expected default mem_mib=1024, got %q", got["mem_mib"])
	}
	if got["vcpus"] != "1" {
		t.Fatalf("expected default vcpus=1, got %q", got["vcpus"])
	}
}

func TestApplySandboxResourcePolicyClampsToMax(t *testing.T) {
	repo := &sandboxSettingsRepoStub{
		ok: true,
		row: store.SandboxResourceSettingsRow{
			DefaultMemoryMiB: 8192,
			DefaultVCPUs:     8,
			MaxMemoryMiB:     2048,
			MaxVCPUs:         2,
		},
	}

	got := applySandboxResourcePolicy(map[string]string{"host_cli_sync": "true"}, repo)
	if got["mem_mib"] != "2048" {
		t.Fatalf("expected clamped mem_mib=2048, got %q", got["mem_mib"])
	}
	if got["vcpus"] != "2" {
		t.Fatalf("expected clamped vcpus=2, got %q", got["vcpus"])
	}
}

func TestApplySandboxResourcePolicyCapsRequestedOverrides(t *testing.T) {
	repo := &sandboxSettingsRepoStub{
		ok: true,
		row: store.SandboxResourceSettingsRow{
			DefaultMemoryMiB: 1024,
			DefaultVCPUs:     1,
			MaxMemoryMiB:     3072,
			MaxVCPUs:         3,
		},
	}

	got := applySandboxResourcePolicy(map[string]string{
		"mem_mib": "4096",
		"vcpus":   "6",
	}, repo)
	if got["mem_mib"] != "3072" {
		t.Fatalf("expected capped mem_mib=3072, got %q", got["mem_mib"])
	}
	if got["vcpus"] != "3" {
		t.Fatalf("expected capped vcpus=3, got %q", got["vcpus"])
	}
}
