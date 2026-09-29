package domain

import "testing"

func TestSandboxHasNICIdentity(t *testing.T) {
	cases := []struct {
		name string
		sb   Sandbox
		want bool
	}{
		{"empty record", Sandbox{}, false},
		{"with socket", Sandbox{VhostSocket: "/s"}, true},
	}
	for _, tc := range cases {
		if got := tc.sb.HasNICIdentity(); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}
