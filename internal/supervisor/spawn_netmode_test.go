package supervisor

import (
	"slices"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
)

func TestBuildSupervisorArgv_CarriesNetMode(t *testing.T) {
	args := BuildSupervisorArgv(SpawnConfig{Config: Config{SandboxRef: "sb-x", NetMode: domain.NetModeVhostUser}})
	i := slices.Index(args, "--net-mode")
	if i < 0 || i+1 >= len(args) || args[i+1] != "vhost-user" {
		t.Fatalf("--net-mode vhost-user missing from argv %v", args)
	}
	if plain := BuildSupervisorArgv(SpawnConfig{Config: Config{SandboxRef: "sb-x"}}); slices.Contains(plain, "--net-mode") {
		t.Fatalf("--net-mode present for a tap record: %v", plain)
	}
}
