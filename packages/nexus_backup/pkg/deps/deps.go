package deps

import (
	"github.com/inizio/nexus/packages/nexus/pkg/config"
	"github.com/inizio/nexus/packages/nexus/pkg/infra/relay"
	"github.com/inizio/nexus/packages/nexus/pkg/project"
	"github.com/inizio/nexus/packages/nexus/pkg/runtime"
	"github.com/inizio/nexus/packages/nexus/pkg/services"
	"github.com/inizio/nexus/packages/nexus/pkg/spotlight"
	"github.com/inizio/nexus/packages/nexus/pkg/store"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

// Deps is the dependency container for the daemon.
// All managers and factories are provided here so transports
// and handlers have a single injection point.
type Deps struct {
	WorkspaceMgr    *workspacemgr.Manager
	ProjectMgr      *project.Manager
	RuntimeFactory  *runtime.Factory
	SpotlightMgr    *spotlight.Manager
	ServiceMgr      *services.Manager
	AuthRelay       *authrelay.Broker
	NodeCfg         *config.NodeConfig
	SandboxSettings store.SandboxResourceSettingsRepository
}

// NewDeps constructs a Deps with the given dependencies.
func NewDeps(
	wsMgr *workspacemgr.Manager,
	projMgr *project.Manager,
	rtFactory *runtime.Factory,
	spotlightMgr *spotlight.Manager,
	svcMgr *services.Manager,
	authRelayBroker *authrelay.Broker,
	nodeCfg *config.NodeConfig,
	sandboxSettings store.SandboxResourceSettingsRepository,
) *Deps {
	return &Deps{
		WorkspaceMgr:    wsMgr,
		ProjectMgr:      projMgr,
		RuntimeFactory:  rtFactory,
		SpotlightMgr:    spotlightMgr,
		ServiceMgr:      svcMgr,
		AuthRelay:       authRelayBroker,
		NodeCfg:         nodeCfg,
		SandboxSettings: sandboxSettings,
	}
}
