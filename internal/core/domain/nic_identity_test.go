package domain

import "testing"

func TestSandboxHasNICIdentity(t *testing.T) {
	cases := []struct {
		name string
		sb   Sandbox
		want bool
	}{
		{"tap with tap name", Sandbox{GuestTapName: "nxg-1"}, true},
		{"tap record with empty GuestTap", Sandbox{}, false},
		{"tap record with only vhost socket", Sandbox{VhostSocket: "/s"}, false},
		{"explicit tap with empty GuestTap", Sandbox{NetMode: NetModeTap, VhostSocket: "/s"}, false},
		{"vhost with socket", Sandbox{NetMode: NetModeVhostUser, VhostSocket: "/s"}, true},
		{"vhost without socket", Sandbox{NetMode: NetModeVhostUser}, false},
		{"vhost with only tap name", Sandbox{NetMode: NetModeVhostUser, GuestTapName: "nxg-1"}, false},
	}
	for _, tc := range cases {
		if got := tc.sb.HasNICIdentity(); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}
