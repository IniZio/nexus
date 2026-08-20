package cli

import (
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// The overlay's job is to let the operator decide what to act on WITHOUT
// dropping to a terminal. Handle and state alone could not answer "does this
// one have my repo mounted?" or "is this an agent sandbox?".
func TestHerdrWorkspaceMounts_NamesWhatIsAttached(t *testing.T) {
	cases := []struct {
		name string
		sb   domain.Sandbox
		want string
	}{
		{
			name: "nothing attached",
			sb:   domain.Sandbox{},
			want: "-",
		},
		{
			name: "live host mount shows its guest path",
			sb: domain.Sandbox{
				LiveMounts: []domain.LiveMount{{HostPath: "/home/me/repo", GuestPath: "/work"}},
			},
			want: "/work",
		},
		{
			name: "named volume shows name and mount point",
			sb: domain.Sandbox{
				MountedVolumes: []domain.VolumeAttachment{{Name: "node_modules", GuestPath: "/work/node_modules"}},
			},
			want: "node_modules→/work/node_modules",
		},
		{
			name: "live mounts are listed before volumes",
			sb: domain.Sandbox{
				LiveMounts:     []domain.LiveMount{{GuestPath: "/work"}},
				MountedVolumes: []domain.VolumeAttachment{{Name: "nm", GuestPath: "/work/nm"}},
			},
			want: "/work,nm→/work/nm",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := herdrWorkspaceMounts(tc.sb); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHerdrWorkspaceAgent_DashWhenNoAgent(t *testing.T) {
	if got := herdrWorkspaceAgent(domain.Sandbox{}); got != "-" {
		t.Errorf("got %q, want %q", got, "-")
	}
	if got := herdrWorkspaceAgent(domain.Sandbox{AgentName: "claude-code"}); got != "claude-code" {
		t.Errorf("got %q, want %q", got, "claude-code")
	}
}

// Column alignment is not cosmetic here: the overlay is a fixed-width terminal
// pane, and ragged columns are what make a list unreadable at a glance. Every
// data row must start each column at the same offset as the header.
func TestHerdrWorkspacesRendering_ColumnsAlign(t *testing.T) {
	// Render through the real helper with deliberately uneven widths, then
	// assert the header offsets are preserved.
	out := renderWorkspaceTable([][6]string{
		{"a/b", "running", "-", "-", "-", "sb-1"},
		{"much-longer/handle", "paused", "claude-code", "/work", "bound", "sb-2"},
	})
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3 (header + 2 rows):\n%s", len(lines), out)
	}
	stateCol := strings.Index(lines[0], "STATE")
	if stateCol < 0 {
		t.Fatalf("header missing STATE column:\n%s", out)
	}
	for i, line := range lines[1:] {
		// The second column must begin exactly where STATE begins.
		if len(line) <= stateCol || line[stateCol] == ' ' {
			t.Errorf("row %d does not align its STATE column at offset %d:\n%s", i, stateCol, out)
		}
	}
}
