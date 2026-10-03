package mcp

import (
	"strings"
	"testing"
)

func TestDelegateWorktreeCreate_SyncMode(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sync    string
		backend string
		want    []string
		errSub  string
	}{
		{"default bundle", "", "sprites", []string{"herdr", "worktree-sandbox", "--backend", "sprites", "wNEW"}, ""},
		{"explicit bundle", "bundle", "sprites", []string{"herdr", "worktree-sandbox", "--backend", "sprites", "wNEW"}, ""},
		{"push", "push", "sprites", []string{"herdr", "worktree-sandbox", "--backend", "sprites", "--sync", "push", "wNEW"}, ""},
		{"unknown", "rsync", "sprites", nil, "unsupported"},
		{"push needs sprites", "push", "", nil, "requires the sprites backend"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NEXUS_BACKEND", "")
			args := map[string]any{"repo_path": t.TempDir(), "branch": "feat/x"}
			if tc.sync != "" {
				args["sync"] = tc.sync
			}
			if tc.backend != "" {
				args["backend"] = tc.backend
			}
			rec, _, text, isErr := runDelegateCreate(t, args)
			if tc.errSub != "" {
				if !isErr || !strings.Contains(text, tc.errSub) {
					t.Fatalf("want error %q, got isErr=%v %q", tc.errSub, isErr, text)
				}
				if len(rec.calls) != 0 {
					t.Fatalf("calls made despite invalid sync: %+v", rec.calls)
				}
				return
			}
			if isErr {
				t.Fatalf("tool returned error: %s", text)
			}
			call, found := rec.find("herdr worktree-sandbox")
			if !found || !argsEqual(call.args, tc.want) {
				t.Fatalf("calls=%+v want argv %q", rec.calls, tc.want)
			}
		})
	}
}
