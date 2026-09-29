package service

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
)

func TestNetModeFromEnv(t *testing.T) {
	cases := []struct {
		env     string
		want    domain.NetMode
		wantErr bool
	}{
		{"", "", false},
		{"tap", domain.NetModeTap, false},
		{"vhost-user", domain.NetModeVhostUser, false},
		{"none", "", true},
		{"bogus", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("NEXUS_NET_MODE", tc.env)
			got, err := NetModeFromEnv()
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Errorf("got %q err %v, want %q wantErr=%v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func withTapProbe(t *testing.T, fn func() error) *int {
	t.Helper()
	calls := 0
	old := TapProbe
	TapProbe = func() error { calls++; return fn() }
	t.Cleanup(func() { TapProbe = old })
	return &calls
}

func TestResolveCreateNetMode(t *testing.T) {
	blocked := func() error { return fmt.Errorf("tap: %w", syscall.EPERM) }
	cases := []struct {
		name      string
		env       string
		probe     func() error
		want      domain.NetMode
		wantErr   bool
		wantCalls int
	}{
		{"unset ok", "", func() error { return nil }, domain.NetModeVhostUser, false, 0},
		{"unset eperm", "", blocked, domain.NetModeVhostUser, false, 0},
		{"unset no probe", "", nil, domain.NetModeVhostUser, false, 0},
		{"tap other error", "tap", func() error { return errors.New("boom") }, domain.NetModeTap, false, 1},
		{"tap eperm", "tap", blocked, "", true, 1},
		{"tap ok", "tap", func() error { return nil }, domain.NetModeTap, false, 1},
		{"vhost-user no probe", "vhost-user", blocked, domain.NetModeVhostUser, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NEXUS_NET_MODE", tc.env)
			if tc.probe == nil {
				old := TapProbe
				TapProbe = nil
				t.Cleanup(func() { TapProbe = old })
			}
			calls := new(int)
			if tc.probe != nil {
				calls = withTapProbe(t, tc.probe)
			}
			got, err := ResolveCreateNetMode()
			if (err != nil) != tc.wantErr || got != tc.want || *calls != tc.wantCalls {
				t.Errorf("got %q err %v calls %d, want %q wantErr=%v calls %d",
					got, err, *calls, tc.want, tc.wantErr, tc.wantCalls)
			}
		})
	}
}

func TestResolveCreateNetMode_ProbeOnlyFromCreate(t *testing.T) {
	src, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range src {
		n := e.Name()
		if !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		b, _ := os.ReadFile(n)
		if n != "service.go" && (strings.Contains(string(b), "TapProbe") || strings.Contains(string(b), "ResolveCreateNetMode()")) && n != "create.go" {
			t.Errorf("%s references the create-time probe; only create.go may", n)
		}
	}
}

func TestLegacyEmptyNetModeStaysTap(t *testing.T) {
	// Empty persisted net_mode is judged by the tap NIC identity, not vhost.
	legacy := domain.Sandbox{GuestTapName: "tap0", VhostSocket: "/run/x.sock"}
	if !legacy.HasNICIdentity() {
		t.Error("empty net_mode with a tap name must be adoptable as tap")
	}
	if (domain.Sandbox{VhostSocket: "/run/x.sock"}).HasNICIdentity() {
		t.Error("empty net_mode must not treat a vhost socket as its NIC identity")
	}
}
