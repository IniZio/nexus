package cloudhypervisor

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
)

func TestTapConfigIgnoresNetModeEnv(t *testing.T) {
	id := domain.NewSandboxID()
	for _, env := range []string{"vhost-user", "none"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv("NEXUS_NET_MODE", env)
			cfg := Config{MemoryMiB: 512}
			nets := buildNets(cfg, "nxg-aabbccddee", id)
			if len(nets) != 1 || nets[0].Tap != "nxg-aabbccddee" || nets[0].VhostUser || nets[0].Socket != "" {
				t.Fatalf("nets = %+v, want one tap device", nets)
			}
			b, err := json.Marshal(buildMemoryConfig(cfg, 512, false))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), "shared") {
				t.Errorf("memory config has shared: %s", b)
			}
		})
	}
	if got := buildNets(Config{NetMode: "none"}, "x", id); got != nil {
		t.Errorf("NetMode none: nets = %+v, want nil", got)
	}
}
