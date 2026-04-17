package daemon

import (
	"context"
	"time"

	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/store"
	"github.com/inizio/nexus/packages/nexus/pkg/workspace"
)

type DaemonSettingsGetParams struct{}

type DaemonSettingsGetResult struct {
	SandboxResources SandboxResourceSettings `json:"sandboxResources"`
}

type DaemonSettingsUpdateParams struct {
	SandboxResources SandboxResourceSettings `json:"sandboxResources"`
}

type DaemonSettingsUpdateResult struct {
	SandboxResources SandboxResourceSettings `json:"sandboxResources"`
}

type SandboxResourceSettings struct {
	DefaultMemoryMiB int `json:"defaultMemoryMiB"`
	DefaultVCPUs     int `json:"defaultVCPUs"`
	MaxMemoryMiB     int `json:"maxMemoryMiB"`
	MaxVCPUs         int `json:"maxVCPUs"`
}

func HandleDaemonSettingsGet(_ context.Context, _ DaemonSettingsGetParams, repo store.SandboxResourceSettingsRepository) (*DaemonSettingsGetResult, *rpckit.RPCError) {
	policy := workspace.SandboxResourcePolicyFromRepository(repo)
	return &DaemonSettingsGetResult{
		SandboxResources: SandboxResourceSettings{
			DefaultMemoryMiB: policy.DefaultMemMiB,
			DefaultVCPUs:     policy.DefaultVCPUs,
			MaxMemoryMiB:     policy.MaxMemMiB,
			MaxVCPUs:         policy.MaxVCPUs,
		},
	}, nil
}

func HandleDaemonSettingsUpdate(_ context.Context, req DaemonSettingsUpdateParams, repo store.SandboxResourceSettingsRepository) (*DaemonSettingsUpdateResult, *rpckit.RPCError) {
	if repo == nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: "settings store unavailable"}
	}
	settings := req.SandboxResources
	if settings.DefaultMemoryMiB <= 0 || settings.DefaultVCPUs <= 0 || settings.MaxMemoryMiB <= 0 || settings.MaxVCPUs <= 0 {
		return nil, rpckit.ErrInvalidParams
	}
	if settings.DefaultMemoryMiB > settings.MaxMemoryMiB || settings.DefaultVCPUs > settings.MaxVCPUs {
		return nil, rpckit.ErrInvalidParams
	}
	err := repo.UpsertSandboxResourceSettings(store.SandboxResourceSettingsRow{
		DefaultMemoryMiB: settings.DefaultMemoryMiB,
		DefaultVCPUs:     settings.DefaultVCPUs,
		MaxMemoryMiB:     settings.MaxMemoryMiB,
		MaxVCPUs:         settings.MaxVCPUs,
		UpdatedAt:        time.Now().UTC(),
	})
	if err != nil {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: err.Error()}
	}
	return &DaemonSettingsUpdateResult{SandboxResources: settings}, nil
}
