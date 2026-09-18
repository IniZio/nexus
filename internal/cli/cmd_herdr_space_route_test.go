package cli

import "testing"

func TestHerdrSpaceLabelForRef(t *testing.T) {
	cases := []struct {
		ref  string
		want string
	}{
		{"demo-orca-01", "nexus:demo-orca-01"},
		{"orca/demo-01", "nexus:orca/demo-01"},
		{"", "nexus:"},
	}
	for _, tc := range cases {
		got := herdrSpaceLabelForRef(tc.ref)
		if got != tc.want {
			t.Errorf("herdrSpaceLabelForRef(%q) = %q, want %q", tc.ref, got, tc.want)
		}
	}
}

func TestHerdrWorkspaceDisplayLabel(t *testing.T) {
	cases := []struct {
		label string
		want  string
	}{
		{"nexus:hanlun-lms/EX-871", "nexus:EX-871"},
		{"nexus:repo/feat-a-b", "nexus:feat-a-b"},
		{"nexus:demo-orca-01", "nexus:demo-orca-01"},
		{"nexus:", "nexus:"},
	}
	for _, tc := range cases {
		if got := herdrWorkspaceDisplayLabel(tc.label); got != tc.want {
			t.Errorf("herdrWorkspaceDisplayLabel(%q) = %q, want %q", tc.label, got, tc.want)
		}
	}
}
