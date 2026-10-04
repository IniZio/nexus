package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/driver/sprites"
)

func TestSpritesLoginLogout(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("SPRITES_TOKEN", "")
	t.Setenv("SPRITES_API_TOKEN", "")
	old := spritesLoginStdin
	defer func() { spritesLoginStdin = old }()

	const secret = "sp_SECRET_VALUE"
	var so, se bytes.Buffer
	out := NewOutput(&so, &se, false)
	spritesLoginStdin = strings.NewReader("export SPRITES_TOKEN=" + secret + "\n")
	if err := runSpritesAuth(context.Background(), []string{"login"}, out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(so.String()+se.String(), secret) {
		t.Fatal("token echoed")
	}
	p, _ := sprites.TokenPath()
	if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("token file: %v %v", st, err)
	}
	if got := sprites.ResolveToken(); got != secret {
		t.Fatalf("resolved %q", got)
	}
	if err := runSpritesAuth(context.Background(), []string{"logout"}, out); err != nil {
		t.Fatal(err)
	}
	if sprites.ResolveToken() != "" {
		t.Fatal("token survives logout")
	}
	spritesLoginStdin = strings.NewReader("")
	if err := runSpritesAuth(context.Background(), []string{"login"}, out); err == nil {
		t.Fatal("empty stdin must fail")
	}
}
