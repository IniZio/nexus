package cloudhypervisor

import (
	"context"
	"encoding/json"
	"path/filepath"
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
			nets := buildNets(cfg, "nxg-aabbccddee", "", id)
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
	if got := buildNets(Config{NetMode: "none"}, "x", "", id); got != nil {
		t.Errorf("NetMode none: nets = %+v, want nil", got)
	}
}

func TestVhostUserNetsAndSharedMemory(t *testing.T) {
	id := domain.NewSandboxID()
	nets := buildNets(Config{NetMode: domain.NetModeVhostUser}, "", "/run/x/vhost.sock", id)
	if len(nets) != 1 || !nets[0].VhostUser || nets[0].Socket != "/run/x/vhost.sock" || nets[0].Tap != "" || nets[0].NumQueues != 2 || nets[0].Mac != sandboxMac(id) {
		t.Fatalf("nets = %+v", nets)
	}
	b, _ := json.Marshal(nets[0])
	for _, want := range []string{`"vhost_user":true`, `"vhost_socket":"/run/x/vhost.sock"`, `"num_queues":2`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("%s missing %s", b, want)
		}
	}
	mb, _ := json.Marshal(buildMemoryConfig(Config{MemoryMiB: 512}, 512, true))
	if !strings.Contains(string(mb), `"shared":true`) {
		t.Errorf("vhost-user memory not shared: %s", mb)
	}
}

func TestSetNetMode(t *testing.T) {
	d := &CHDriver{}
	d.SetNetMode(domain.NetModeVhostUser)
	if d.cfg.NetMode != domain.NetModeVhostUser {
		t.Fatalf("NetMode = %q", d.cfg.NetMode)
	}
}

func TestStartNetnsRuntimeFollowsCfgNotEnv(t *testing.T) {
	cases := []struct {
		env   string
		mode  domain.NetMode
		vhost bool
	}{
		{"vhost-user", "", false},
		{"vhost-user", domain.NetModeTap, false},
		{"tap", domain.NetModeVhostUser, true},
		{"", domain.NetModeVhostUser, true},
	}
	for _, c := range cases {
		t.Run(c.env+"/"+string(c.mode), func(t *testing.T) {
			t.Setenv("NEXUS_NET_MODE", c.env)
			id := domain.NewSandboxID()
			dir := t.TempDir()
			cfg := Config{BinaryPath: "/nonexistent", SocketDir: dir, NetMode: c.mode}
			rt, err := StartNetnsRuntime(context.Background(), cfg, id, filepath.Join(dir, "ch.sock"), "")
			if err != nil {
				t.Skipf("cannot spawn netns child here: %v", err)
			}
			defer rt.Stop()
			if got := rt.VhostSocket != ""; got != c.vhost {
				t.Fatalf("vhost socket set = %v, want %v", got, c.vhost)
			}
			if c.vhost != (rt.GuestTap == "") {
				t.Fatalf("GuestTap = %q with vhost=%v", rt.GuestTap, c.vhost)
			}
			env := strings.Join(rt.cmd.Env, "\n")
			if strings.Contains(env, "NEXUS_NET_MODE=") {
				t.Errorf("child env leaks NEXUS_NET_MODE: %s", env)
			}
			if got := strings.Contains(env, netnsEnvNetMode+"=vhost-user"); got != c.vhost {
				t.Errorf("child mode marker present = %v, want %v", got, c.vhost)
			}
			if got := strings.Contains(env, netnsEnvGuestTap+"="); got == c.vhost {
				t.Errorf("child tap env present = %v with vhost=%v", got, c.vhost)
			}
		})
	}
}
