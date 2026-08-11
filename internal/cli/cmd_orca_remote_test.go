package cli

import (
	"testing"
)

func TestParseSSHTarget(t *testing.T) {
	cases := []struct {
		in      string
		dest    string
		port    int
		wantErr bool
	}{
		{"user@host", "user@host", 0, false},
		{"host", "host", 0, false},
		{"ssh://u@h:2222", "u@h", 2222, false},
		{"", "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseSSHTarget(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.dest != tc.dest {
				t.Errorf("dest: got %q, want %q", got.dest, tc.dest)
			}
			if got.port != tc.port {
				t.Errorf("port: got %d, want %d", got.port, tc.port)
			}
		})
	}
}

func TestResolveOrcaRemote_StripsFlag(t *testing.T) {
	// Isolate from any ambient env that could cause resolveOrcaRemote to pick
	// up a real remote even when the test expects remote==nil.
	t.Setenv("NEXUS3_REMOTE", "")
	t.Setenv("NEXUS3_ORCA_REMOTE_INNER", "")

	// Space form: --remote user@host create
	remote, rest, err := resolveOrcaRemote([]string{"--remote", "user@host", "create"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if remote == nil {
		t.Fatal("expected non-nil remote")
	}
	if remote.dest != "user@host" {
		t.Errorf("dest: got %q", remote.dest)
	}
	if len(rest) != 1 || rest[0] != "create" {
		t.Errorf("rest: got %v", rest)
	}

	// Equals form: --remote=user@host create
	remote2, rest2, err2 := resolveOrcaRemote([]string{"--remote=user@host", "create"})
	if err2 != nil {
		t.Fatalf("unexpected error: %v", err2)
	}
	if remote2 == nil || remote2.dest != "user@host" {
		t.Errorf("equals form: remote=%v", remote2)
	}
	if len(rest2) != 1 || rest2[0] != "create" {
		t.Errorf("equals form: rest=%v", rest2)
	}

	// No flag, no env → remote==nil.
	remote3, rest3, err3 := resolveOrcaRemote([]string{"create"})
	if err3 != nil {
		t.Fatalf("unexpected error: %v", err3)
	}
	if remote3 != nil {
		t.Errorf("expected nil remote, got %v", remote3)
	}
	if len(rest3) != 1 || rest3[0] != "create" {
		t.Errorf("rest: got %v", rest3)
	}

	// Empty-string NEXUS3_REMOTE (e.g. exported but blank) → remote==nil.
	t.Setenv("NEXUS3_REMOTE", "   ")
	remote4, _, err4 := resolveOrcaRemote([]string{"create"})
	if err4 != nil {
		t.Fatalf("unexpected error: %v", err4)
	}
	if remote4 != nil {
		t.Errorf("whitespace NEXUS3_REMOTE: expected nil remote, got %v", remote4)
	}
	t.Setenv("NEXUS3_REMOTE", "") // reset for subsequent sub-tests
}

func TestResolveOrcaRemote_EnvAndInnerGuard(t *testing.T) {
	// Clear ambient env before any sub-test sets it explicitly.
	t.Setenv("NEXUS3_REMOTE", "")
	t.Setenv("NEXUS3_ORCA_REMOTE_INNER", "")

	// NEXUS3_REMOTE set → remote != nil.
	t.Run("env set", func(t *testing.T) {
		t.Setenv("NEXUS3_ORCA_REMOTE_INNER", "")
		t.Setenv("NEXUS3_REMOTE", "user@host")
		remote, rest, err := resolveOrcaRemote([]string{"create"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if remote == nil {
			t.Fatal("expected non-nil remote from env")
		}
		if rest[0] != "create" {
			t.Errorf("rest: %v", rest)
		}
	})

	// NEXUS3_ORCA_REMOTE_INNER=1 + NEXUS3_REMOTE → forced local (remote==nil).
	t.Run("inner guard", func(t *testing.T) {
		t.Setenv("NEXUS3_REMOTE", "user@host") // must be overridden by inner guard
		t.Setenv("NEXUS3_ORCA_REMOTE_INNER", "1")
		remote, rest, err := resolveOrcaRemote([]string{"create"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if remote != nil {
			t.Errorf("expected nil remote (inner guard), got %v", remote)
		}
		if len(rest) != 1 || rest[0] != "create" {
			t.Errorf("rest: %v", rest)
		}
	})
}

func TestProxyPrefix(t *testing.T) {
	t1 := &sshTarget{dest: "user@host", port: 0}
	if got := t1.proxyPrefix(); got != "ssh user@host" {
		t.Errorf("port 0: got %q", got)
	}

	t2 := &sshTarget{dest: "user@host", port: 2222}
	if got := t2.proxyPrefix(); got != "ssh -p 2222 user@host" {
		t.Errorf("port 2222: got %q", got)
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("a b"); got != "'a b'" {
		t.Errorf("space: got %q", got)
	}
	if got := shellQuote("it's"); got != `'it'\''s'` {
		t.Errorf("quote: got %q", got)
	}
}

func TestBuildRemoteCmd(t *testing.T) {
	got := buildRemoteCmd(
		[]string{"ORCA_X=1", "NEXUS3_IMAGE=img"},
		"/bin/nexus3", "orca", "create",
	)
	want := "env 'ORCA_X=1' 'NEXUS3_IMAGE=img' '/bin/nexus3' 'orca' 'create'"
	if got != want {
		t.Errorf("got:  %q\nwant: %q", got, want)
	}
}
