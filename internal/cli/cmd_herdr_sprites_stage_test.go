package cli

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/IniZio/nexus/internal/core/agent"
	"github.com/IniZio/nexus/internal/core/domain"
)

type fakeSpritesFS struct {
	files  map[string]string
	writes int
}

func (f *fakeSpritesFS) Get(context.Context, string) (domain.Sandbox, error) {
	return domain.Sandbox{}, nil
}

func (f *fakeSpritesFS) Exec(_ context.Context, _ string, o agent.ExecOptions) (int32, error) {
	rel := o.Argv[len(o.Argv)-1]
	if o.Stdin != nil {
		b, _ := io.ReadAll(o.Stdin)
		f.files[rel] = string(b)
		f.writes++
		return 0, nil
	}
	if v, ok := f.files[rel]; ok {
		_, _ = io.WriteString(o.Stdout, v)
	}
	return 0, nil
}

func decodeObj(t *testing.T, s string) map[string]any {
	t.Helper()
	m := map[string]any{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("bad json %q: %v", s, err)
	}
	return m
}

func TestStageSpritesClaude_MergesIntoExistingFiles(t *testing.T) {
	fs := &fakeSpritesFS{files: map[string]string{
		spritesClaudeSettingsPath: `{"hooks":{"Stop":[]},"model":"x"}`,
		spritesClaudeStatePath:    `{"theme":"light","userID":"u","projects":{"/p":{"allowedTools":["Bash"],"lastCost":1},"/other":{"k":1}}}`,
	}}
	for i := 0; i < 2; i++ {
		if err := herdrStageSpritesClaude(context.Background(), "r", "/p", fs); err != nil {
			t.Fatal(err)
		}
	}
	s := decodeObj(t, fs.files[spritesClaudeSettingsPath])
	if s["skipDangerousModePermissionPrompt"] != true || s["model"] != "x" || s["hooks"] == nil {
		t.Errorf("settings = %v", s)
	}
	c := decodeObj(t, fs.files[spritesClaudeStatePath])
	if c["hasCompletedOnboarding"] != true || c["theme"] != "light" || c["userID"] != "u" {
		t.Errorf("state = %v", c)
	}
	projs := c["projects"].(map[string]any)
	p := projs["/p"].(map[string]any)
	if p["hasTrustDialogAccepted"] != true || p["hasCompletedProjectOnboarding"] != true || p["lastCost"] != float64(1) {
		t.Errorf("project = %v", p)
	}
	if len(p["allowedTools"].([]any)) != 1 || projs["/other"] == nil {
		t.Errorf("projects = %v", projs)
	}
}

func TestStageSpritesClaude_CreatesMissingFilesIdempotent(t *testing.T) {
	fs := &fakeSpritesFS{files: map[string]string{}}
	if err := herdrStageSpritesClaude(context.Background(), "r", "/p", fs); err != nil {
		t.Fatal(err)
	}
	first := fs.files[spritesClaudeStatePath]
	if err := herdrStageSpritesClaude(context.Background(), "r", "/p", fs); err != nil {
		t.Fatal(err)
	}
	if fs.files[spritesClaudeStatePath] != first {
		t.Error("not idempotent")
	}
	c := decodeObj(t, first)
	if c["theme"] != "dark" || c["hasCompletedOnboarding"] != true {
		t.Errorf("state = %v", c)
	}
	if decodeObj(t, fs.files[spritesClaudeSettingsPath])["skipDangerousModePermissionPrompt"] != true {
		t.Error("settings missing key")
	}
}

func TestStageSpritesClaude_RefusesNonObject(t *testing.T) {
	fs := &fakeSpritesFS{files: map[string]string{spritesClaudeSettingsPath: `[1]`}}
	if err := herdrStageSpritesClaude(context.Background(), "r", "/p", fs); err == nil {
		t.Fatal("want error")
	}
	if fs.writes != 0 {
		t.Error("must not overwrite unparseable file")
	}
}
