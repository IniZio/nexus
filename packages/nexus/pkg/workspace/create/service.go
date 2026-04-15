package create

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/inizio/nexus/packages/nexus/pkg/config"
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
	if localDriverEnabledForRepo(spec.Repo) {
		spec.Backend = "local"
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

func localDriverEnabledForRepo(repo string) bool {
	repoRoot := strings.TrimSpace(repo)
	if repoRoot == "" {
		return false
	}
	if !filepath.IsAbs(repoRoot) {
		abs, err := filepath.Abs(repoRoot)
		if err != nil {
			return false
		}
		repoRoot = abs
	}
	info, err := os.Stat(repoRoot)
	if err != nil || !info.IsDir() {
		return false
	}
	workspaceJSONPath := filepath.Join(repoRoot, ".nexus", "workspace.json")
	if _, err := os.Stat(workspaceJSONPath); err != nil {
		return false
	}
	cfg, _, err := config.LoadWorkspaceConfig(repoRoot)
	if err != nil {
		return false
	}
	return cfg.InternalFeatures.LocalDriver
}
