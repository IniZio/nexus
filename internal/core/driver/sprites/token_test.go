package sprites

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tokenEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("SPRITES_TOKEN", "")
	t.Setenv("SPRITES_API_TOKEN", "")
}

func TestParseToken(t *testing.T) {
	for in, want := range map[string]string{
		"abc123\n":                        "abc123",
		"SPRITES_TOKEN=abc123":            "abc123",
		"export SPRITES_TOKEN=abc123\n":   "abc123",
		"# c\nexport SPRITES_TOKEN=\"x\"": "x",
		"SPRITES_API_TOKEN='y'":           "y",
	} {
		got, err := ParseToken(in)
		if err != nil || got != want {
			t.Errorf("ParseToken(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "\n", "SPRITES_TOKEN=", "two words"} {
		if _, err := ParseToken(in); err == nil {
			t.Errorf("ParseToken(%q): want error", in)
		}
	}
}

func TestSaveTokenPermsAndRemove(t *testing.T) {
	tokenEnv(t)
	if err := SaveToken("tok1"); err != nil {
		t.Fatal(err)
	}
	p, _ := TokenPath()
	st, err := os.Stat(p)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v, %v", st, err)
	}
	if d, _ := os.Stat(filepath.Dir(p)); d.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %v", d.Mode().Perm())
	}
	if err := SaveToken("tok2"); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(filepath.Dir(p)); len(ents) != 1 {
		t.Fatalf("temp file left behind: %v", ents)
	}
	if got := ResolveToken(); got != "tok2" {
		t.Fatalf("ResolveToken = %q", got)
	}
	if err := RemoveToken(); err != nil {
		t.Fatal(err)
	}
	if err := RemoveToken(); err != nil {
		t.Fatalf("second remove: %v", err)
	}
	if got := ResolveToken(); got != "" {
		t.Fatalf("after remove = %q", got)
	}
}

func TestResolveTokenOrder(t *testing.T) {
	tokenEnv(t)
	if err := SaveToken("fromfile"); err != nil {
		t.Fatal(err)
	}
	if got := ResolveToken(); got != "fromfile" {
		t.Fatalf("file only = %q", got)
	}
	t.Setenv("SPRITES_API_TOKEN", "apienv")
	if got := ResolveToken(); got != "apienv" {
		t.Fatalf("api env > file = %q", got)
	}
	t.Setenv("SPRITES_TOKEN", "env")
	if got := ResolveToken(); got != "env" {
		t.Fatalf("env > file = %q", got)
	}
}

func TestNewUsesTokenFileAndErrorNamesBoth(t *testing.T) {
	tokenEnv(t)
	_, err := New(Config{StateDir: t.TempDir()})
	if err == nil {
		t.Fatal("want error without token")
	}
	for _, s := range []string{"SPRITES_TOKEN", "nexus sprites login"} {
		if !strings.Contains(err.Error(), s) {
			t.Fatalf("error %q lacks %q", err, s)
		}
	}
	if err := SaveToken("filetok"); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{StateDir: t.TempDir()}); err != nil {
		t.Fatalf("token file not used: %v", err)
	}
}
