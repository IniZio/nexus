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
		{"unset ok", "", func() error { return nil }, "", false, 1},
		{"unset eperm", "", blocked, domain.NetModeVhostUser, false, 1},
		{"unset other error", "", func() error { return errors.New("boom") }, "", false, 1},
		{"tap eperm", "tap", blocked, "", true, 1},
		{"tap ok", "tap", func() error { return nil }, domain.NetModeTap, false, 1},
		{"vhost-user no probe", "vhost-user", blocked, domain.NetModeVhostUser, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NEXUS_NET_MODE", tc.env)
			calls := withTapProbe(t, tc.probe)
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
