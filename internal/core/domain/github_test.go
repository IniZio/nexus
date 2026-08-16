package domain

import "testing"

func TestIsGitHubHost(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		// canonical forms
		{"github.com", true},
		{"api.github.com", true},
		{"uploads.github.com", true},
		{"codeload.github.com", true},
		{"raw.githubusercontent.com", true},
		// bare githubusercontent.com (was previously missed)
		{"githubusercontent.com", true},
		{"GITHUBUSERCONTENT.COM", true},
		// trailing-dot FQDN forms (were previously missed)
		{"api.github.com.", true},
		{"github.com.", true},
		{"githubusercontent.com.", true},
		{"raw.githubusercontent.com.", true},
		// non-GitHub
		{"internal.example.com", false},
		{"notgithub.com", false},
		{"fakegithub.com", false},
		{"api.github.com.evil.com", false},
		{"", false},
	}
	for _, tc := range tests {
		got := IsGitHubHost(tc.in)
		if got != tc.want {
			t.Errorf("IsGitHubHost(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
