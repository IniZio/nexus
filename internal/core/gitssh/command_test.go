package gitssh_test

import (
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/gitssh"
)

func TestParseCommand(t *testing.T) {
	tests := []struct {
		name      string
		argv      []string
		wantErr   bool
		errSubstr string
		wantCmd   *gitssh.ParsedCommand
	}{
		{
			name: "receive-pack quoted path",
			argv: []string{"git@github.com", "git-receive-pack '/example-org/example-app.git'"},
			wantCmd: &gitssh.ParsedCommand{
				Service:   "git-receive-pack",
				GitHost:   "git@github.com",
				BareHost:  "github.com",
				OwnerRepo: "example-org/example-app",
				RawPath:   "/example-org/example-app.git",
			},
		},
		{
			name: "upload-pack quoted path",
			argv: []string{"git@github.com", "git-upload-pack '/example-org/example-app.git'"},
			wantCmd: &gitssh.ParsedCommand{
				Service:   "git-upload-pack",
				GitHost:   "git@github.com",
				BareHost:  "github.com",
				OwnerRepo: "example-org/example-app",
				RawPath:   "/example-org/example-app.git",
			},
		},
		{
			// MUTATION-PIN: removing the leading-dash check in ParseCommand makes
			// this subtest pass when it must fail.
			name:      "flag injection rejected",
			argv:      []string{"-oProxyCommand=evil", "git@github.com", "git-receive-pack '/x/y.git'"},
			wantErr:   true,
			errSubstr: "SSH option",
		},
		{
			// MUTATION-PIN: removing the leading-dash check in ParseCommand makes
			// this subtest pass when it must fail.
			name:      "port flag injection rejected",
			argv:      []string{"-p", "22", "git@github.com", "git-upload-pack '/x/y.git'"},
			wantErr:   true,
			errSubstr: "SSH option",
		},
		{
			name:      "too short — missing command",
			argv:      []string{"git@github.com"},
			wantErr:   true,
			errSubstr: "too short",
		},
		{
			name:      "empty argv",
			argv:      []string{},
			wantErr:   true,
			errSubstr: "too short",
		},
		{
			name:      "non-git command refused",
			argv:      []string{"git@github.com", "bash -c 'evil'"},
			wantErr:   true,
			errSubstr: "git-receive-pack or git-upload-pack",
		},
		{
			name:      "git-archive refused",
			argv:      []string{"git@github.com", "git-archive --remote=/x/y.git HEAD"},
			wantErr:   true,
			errSubstr: "git-receive-pack or git-upload-pack",
		},
		{
			// Git SSH URLs (git@host:owner/repo.git) produce a path WITHOUT a
			// leading slash. The relay normalises these by prepending "/".
			name: "path without leading slash accepted and normalised",
			argv: []string{"git@github.com", "git-upload-pack 'owner/repo.git'"},
			wantCmd: &gitssh.ParsedCommand{
				Service:   "git-upload-pack",
				GitHost:   "git@github.com",
				BareHost:  "github.com",
				OwnerRepo: "owner/repo",
				RawPath:   "/owner/repo.git",
			},
		},
		{
			name:      "plain ssh command refused",
			argv:      []string{"git@github.com", "whoami"},
			wantErr:   true,
			errSubstr: "git-receive-pack or git-upload-pack",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := gitssh.ParseCommand(tc.argv)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.errSubstr)
				}
				if tc.errSubstr != "" && !strings.Contains(err.Error(), tc.errSubstr) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.errSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantCmd != nil {
				if got.Service != tc.wantCmd.Service {
					t.Errorf("Service: got %q, want %q", got.Service, tc.wantCmd.Service)
				}
				if got.GitHost != tc.wantCmd.GitHost {
					t.Errorf("GitHost: got %q, want %q", got.GitHost, tc.wantCmd.GitHost)
				}
				if got.BareHost != tc.wantCmd.BareHost {
					t.Errorf("BareHost: got %q, want %q", got.BareHost, tc.wantCmd.BareHost)
				}
				if got.OwnerRepo != tc.wantCmd.OwnerRepo {
					t.Errorf("OwnerRepo: got %q, want %q", got.OwnerRepo, tc.wantCmd.OwnerRepo)
				}
				if got.RawPath != tc.wantCmd.RawPath {
					t.Errorf("RawPath: got %q, want %q", got.RawPath, tc.wantCmd.RawPath)
				}
			}
		})
	}
}
