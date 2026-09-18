package herdrout

import "testing"

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
