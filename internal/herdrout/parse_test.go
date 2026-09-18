package herdrout

import (
	"testing"
)

func TestWorktreeCreateWorkspaceID(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "legacy ws line",
			in:   `{"ws":"wNEW","linked":true}` + "\n",
			want: "wNEW",
		},
		{
			name: "herdr 0.9.0 result envelope",
			in: `{"id":"cli:worktree:create","result":{"root_pane":{},"tab":{},"type":"worktree_created",` +
				`"workspace":{"active_tab_id":"wA2:t1","label":"nexus-probe-tmp","workspace_id":"wA2","worktree":{}}` +
				`,"worktree":{"branch":"nexus-probe-tmp","is_linked_worktree":true,"open_workspace_id":"wA2","path":"/home/newman/.herdr/worktrees/groundwork/nexus-probe-tmp"}}}` + "\n",
			want: "wA2",
		},
		{
			name: "no id line",
			in:   "not json\nstill not json\n",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := WorktreeCreateWorkspaceID(tc.in)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWorktreePath(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		branch string
		want   string
	}{
		{
			name:   "found",
			in:     `{"result":{"worktrees":[{"branch":"feat/x","path":"/wt/path"}]}}`,
			branch: "feat/x",
			want:   "/wt/path",
		},
		{
			name:   "not found",
			in:     `{"result":{"worktrees":[{"branch":"main","path":"/wt/main"}]}}`,
			branch: "feat/x",
			want:   "",
		},
		{
			name:   "empty output",
			in:     "",
			branch: "feat/x",
			want:   "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := WorktreePath(tc.in, tc.branch)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWorktreePathByWorkspaceID(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		workspaceID string
		want        string
	}{
		{
			name:        "found",
			in:          `{"result":{"worktrees":[{"path":"/wt/path","open_workspace_id":"wA2"}]}}`,
			workspaceID: "wA2",
			want:        "/wt/path",
		},
		{
			name:        "not found",
			in:          `{"result":{"worktrees":[{"path":"/wt/main","open_workspace_id":"wOTHER"}]}}`,
			workspaceID: "wA2",
			want:        "",
		},
		{
			name:        "empty output",
			in:          "",
			workspaceID: "wA2",
			want:        "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := WorktreePathByWorkspaceID(tc.in, tc.workspaceID)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseHerdrErrorCode(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantCode string
		wantMsg  string
		wantOK   bool
	}{
		{
			name:     "dirty worktree error",
			in:       `{"error":{"code":"dirty_worktree_requires_force","message":"fatal: '/tmp/wt' contains modified or untracked files"},"id":"cli:worktree:remove"}`,
			wantCode: "dirty_worktree_requires_force",
			wantMsg:  "fatal: '/tmp/wt' contains modified or untracked files",
			wantOK:   true,
		},
		{
			name:     "other error",
			in:       `{"error":{"code":"workspace_not_found","message":"workspace wX not found"},"id":"cli:worktree:remove"}`,
			wantCode: "workspace_not_found",
			wantMsg:  "workspace wX not found",
			wantOK:   true,
		},
		{
			name:   "no json",
			in:     "exit status 1\nsome plain text\n",
			wantOK: false,
		},
		{
			name:   "json without error code",
			in:     `{"result":{"ok":true}}`,
			wantOK: false,
		},
		{
			name:     "error on second line",
			in:       "preamble text\n" + `{"error":{"code":"some_code","message":"some msg"},"id":"x"}`,
			wantCode: "some_code",
			wantMsg:  "some msg",
			wantOK:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, msg, ok := ParseHerdrErrorCode(tc.in)
			if ok != tc.wantOK {
				t.Errorf("ok=%v want %v", ok, tc.wantOK)
			}
			if code != tc.wantCode {
				t.Errorf("code=%q want %q", code, tc.wantCode)
			}
			if msg != tc.wantMsg {
				t.Errorf("msg=%q want %q", msg, tc.wantMsg)
			}
		})
	}
}
