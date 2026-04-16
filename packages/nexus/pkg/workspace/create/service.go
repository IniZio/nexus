package create

import (
	"context"
	"fmt"
	goruntime "runtime"

	"github.com/inizio/nexus/packages/nexus/pkg/config"
	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime/selection"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

func PrepareCreate(ctx context.Context, spec workspacemgr.CreateSpec, factory *runtime.Factory) (workspacemgr.CreateSpec, *rpckit.RPCError, bool) {
	if factory == nil {
		return spec, nil, false
	}

	// Load workspace config for selection (nil cfg is OK — SelectBackend uses defaults).
	var cfg *config.WorkspaceConfig
	if spec.Repo != "" {
		if c, _, err := config.LoadWorkspaceConfig(spec.Repo); err == nil {
			cfg = &c
		}
	}

	backend, mode, err := selection.SelectBackend(goruntime.GOOS, cfg)
	if err != nil {
		return workspacemgr.CreateSpec{}, &rpckit.RPCError{Code: -32603, Message: fmt.Sprintf("backend selection failed: %v", err)}, false
	}
	spec.Backend = backend
	_ = mode // mode is carried separately; backend name is sufficient for spec routing
	return spec, nil, false
}
