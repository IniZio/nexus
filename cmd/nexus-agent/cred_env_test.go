package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadEnvFile(t *testing.T) {
	cases := []struct {
		name    string
		content string
		unquote bool
		want    map[string]string
	}{
		{
			name:    "raw no unquote",
			content: "A=1\nB=2\n",
			unquote: false,
			want:    map[string]string{"A": "1", "B": "2"},
		},
		{
			name:    "unquote strips quotes",
			content: "A='val'\nB=\"dq\"\nC=plain\n",
			unquote: true,
			want:    map[string]string{"A": "val", "B": "dq", "C": "plain"},
		},
		{
			name:    "raw preserves quotes",
			content: "A='val'\n",
			unquote: false,
			want:    map[string]string{"A": "'val'"},
		},
		{
			name:    "missing file returns nil",
			content: "",
			unquote: false,
			want:    nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var path string
			if tc.want == nil {
				path = filepath.Join(t.TempDir(), "absent.env")
			} else {
				f, err := os.CreateTemp(t.TempDir(), "env-*")
				if err != nil {
					t.Fatal(err)
				}
				f.WriteString(tc.content)
				f.Close()
				path = f.Name()
			}
			got := readEnvFile(path, tc.unquote)
			if tc.want == nil {
				if got != nil {
					t.Errorf("want nil, got %v", got)
				}
				return
			}
			for k, want := range tc.want {
				if v, ok := got[k]; !ok {
					t.Errorf("key %s missing", k)
				} else if v != want {
					t.Errorf("%s = %q; want %q", k, v, want)
				}
			}
		})
	}
}

func TestGuestBaselineEnvCredFile(t *testing.T) {
	dir := t.TempDir()
	credFile := filepath.Join(dir, "cred.env")
	if err := os.WriteFile(credFile, []byte("CLAUDE_CODE_OAUTH_TOKEN=abc\n"), 0600); err != nil {
		t.Fatal(err)
	}

	orig := nexusCredEnvPath
	nexusCredEnvPath = credFile
	t.Cleanup(func() { nexusCredEnvPath = orig })

	env := guestBaselineEnv(false)
	first := envFirstValues(env)
	if got, ok := first["CLAUDE_CODE_OAUTH_TOKEN"]; !ok {
		t.Error("CLAUDE_CODE_OAUTH_TOKEN missing from guestBaselineEnv with cred.env present")
	} else if got != "abc" {
		t.Errorf("CLAUDE_CODE_OAUTH_TOKEN = %q; want abc", got)
	}
}

func TestGuestBaselineEnvCredFileMissing(t *testing.T) {
	orig := nexusCredEnvPath
	nexusCredEnvPath = "/nonexistent/cred.env"
	t.Cleanup(func() { nexusCredEnvPath = orig })

	env := guestBaselineEnv(false)
	first := envFirstValues(env)
	if _, ok := first["CLAUDE_CODE_OAUTH_TOKEN"]; ok {
		t.Error("CLAUDE_CODE_OAUTH_TOKEN present but cred.env is missing")
	}
}

func TestGuestBaselineEnvCredFileUnquotesValues(t *testing.T) {
	dir := t.TempDir()
	credFile := filepath.Join(dir, "cred.env")
	content := "A='Bearer x'\nB=\"dq val\"\nC=plain\n"
	if err := os.WriteFile(credFile, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	orig := nexusCredEnvPath
	nexusCredEnvPath = credFile
	t.Cleanup(func() { nexusCredEnvPath = orig })

	env := guestBaselineEnv(false)
	first := envFirstValues(env)
	cases := map[string]string{"A": "Bearer x", "B": "dq val", "C": "plain"}
	for k, want := range cases {
		if got, ok := first[k]; !ok {
			t.Errorf("%s missing from guestBaselineEnv", k)
		} else if got != want {
			t.Errorf("%s = %q; want %q", k, got, want)
		}
	}
}

func TestGuestBaselineEnvCredCallerWins(t *testing.T) {
	dir := t.TempDir()
	credFile := filepath.Join(dir, "cred.env")
	if err := os.WriteFile(credFile, []byte("CLAUDE_CODE_OAUTH_TOKEN=from-cred\n"), 0600); err != nil {
		t.Fatal(err)
	}

	orig := nexusCredEnvPath
	nexusCredEnvPath = credFile
	t.Cleanup(func() { nexusCredEnvPath = orig })

	env := mergeEnv(guestBaselineEnv(false), map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "caller-override"})
	first := envFirstValues(env)
	if got := first["CLAUDE_CODE_OAUTH_TOKEN"]; got != "caller-override" {
		t.Errorf("CLAUDE_CODE_OAUTH_TOKEN = %q; want caller-override", got)
	}
}
