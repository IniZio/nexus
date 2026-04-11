package create

import (
	"context"

	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime/selection"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

func DefaultPlatformHints() ([]string, []string) {
	return []string{"darwin", "linux"}, nil
}

func PrepareCreate(ctx context.Context, spec workspacemgr.CreateSpec, factory *runtime.Factory) (workspacemgr.CreateSpec, *rpckit.RPCError, bool) {
	if factory == nil {
		return spec, nil, false
	}
	requiredBackends, requiredCaps := DefaultPlatformHints()
	backend, selErr := selection.SelectBackend(ctx, spec.Repo, requiredBackends, requiredCaps, factory)
	if selErr != nil {
		return workspacemgr.CreateSpec{}, selErr, false
	}
	spec.Backend = backend
	return spec, nil, false
}
