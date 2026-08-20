package cli

import (
	"reflect"
	"testing"
)

// The documented form of both `exec` and `run` is
//
//	nexus3 exec <ref> -- <command> [args...]
//
// and it did not work: flag.Parse stops at the first positional, so "--" was
// never consumed and became the executable name inside the guest —
// `exec: "--": executable file not found in $PATH`. 19 invocations across the
// manual use that shape.
func TestStripArgvSeparator(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "documented form: leading separator is removed",
			in:   []string{"--", "uname", "-r"},
			want: []string{"uname", "-r"},
		},
		{
			name: "no separator: argv is untouched",
			in:   []string{"uname", "-r"},
			want: []string{"uname", "-r"},
		},
		{
			name: "empty argv",
			in:   []string{},
			want: []string{},
		},
		{
			// `nexus3 exec box -- git log --` must reach git intact. A later
			// separator is the guest command's, not ours.
			name: "trailing separator belongs to the guest command",
			in:   []string{"--", "git", "log", "--"},
			want: []string{"git", "log", "--"},
		},
		{
			// `nexus3 exec box -- -- weird` — only ONE is ours. Stripping both
			// would silently rewrite the operator's command.
			name: "only the first separator is stripped",
			in:   []string{"--", "--", "weird"},
			want: []string{"--", "weird"},
		},
		{
			// A separator that is not in the leading position is data.
			name: "interior separator is preserved",
			in:   []string{"sh", "-c", "--", "cmd"},
			want: []string{"sh", "-c", "--", "cmd"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripArgvSeparator(tc.in)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
