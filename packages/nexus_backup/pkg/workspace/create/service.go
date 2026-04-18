package create

import (
	"context"
	"strings"

	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

func PrepareCreate(_ context.Context, spec workspacemgr.CreateSpec, _ *runtime.Factory) (workspacemgr.CreateSpec, bool) {
	if strings.TrimSpace(spec.Backend) == "" {
		spec.Backend = "firecracker"
	}
	return spec, false
}
