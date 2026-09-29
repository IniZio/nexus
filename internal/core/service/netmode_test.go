package service

import (
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
