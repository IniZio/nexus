package domain

import "testing"

func TestSandboxHasNICIdentity(t *testing.T) {
	cases := []struct {
		name string
		sb   Sandbox
		want bool
	}{
		{"empty record", Sandbox{}, false},
		{"socket without mode", Sandbox{VhostSocket: "/s"}, true},
		{"vhost with socket", Sandbox{NetMode: NetModeVhostUser, VhostSocket: "/s"}, true},
		{"vhost without socket", Sandbox{NetMode: NetModeVhostUser}, false},
	}
	for _, tc := range cases {
		if got := tc.sb.HasNICIdentity(); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}
