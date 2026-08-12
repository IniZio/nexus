package cli

import "testing"

func TestHerdrSpaceLabelForRef(t *testing.T) {
	cases := []struct {
		ref  string
		want string
	}{
		{"demo-orca-01", "nexus3:demo-orca-01"},
		{"orca/demo-01", "nexus3:orca/demo-01"},
		{"", "nexus3:"},
	}
	for _, tc := range cases {
		got := herdrSpaceLabelForRef(tc.ref)
		if got != tc.want {
			t.Errorf("herdrSpaceLabelForRef(%q) = %q, want %q", tc.ref, got, tc.want)
		}
	}
}
