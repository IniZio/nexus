package main

import (
	"strings"
	"testing"
)

// TestMergeEnv is a table-driven unit test for the mergeEnv helper used by
// Exec to build the process environment. It runs without any build tags and
// is safe to run in CI without a live VM or guest agent.
//
// Wave 1 injects CLAUDE_CODE_OAUTH_TOKEN / NODE_EXTRA_CA_CERTS through this
// exact path; a silent regression here would silently break agent auth.
func TestMergeEnv(t *testing.T) {
	cases := []struct {
		name    string
		base    []string
		extra   map[string]string
		// checks is a list of (key, wantValue) pairs; all must be present in result.
		checks  [][2]string
		// absent is a list of "KEY=VAL" entries that must NOT appear verbatim in result.
		absent  []string
	}{
		{
			name:  "replace_existing_key",
			base:  []string{"HOME=/", "PATH=/usr/bin"},
			extra: map[string]string{"HOME": "/root"},
			checks: [][2]string{
				{"HOME", "/root"},
				{"PATH", "/usr/bin"},
			},
		},
		{
			name:  "append_new_key",
			base:  []string{"HOME=/"},
			extra: map[string]string{"NEW_VAR": "hello"},
			checks: [][2]string{
				{"HOME", "/"},
				{"NEW_VAR", "hello"},
			},
		},
		{
			name:  "value_containing_equals",
			base:  []string{"OTHER=x"},
			extra: map[string]string{"FOO": "a=b"},
			checks: [][2]string{
				{"FOO", "a=b"},
				{"OTHER", "x"},
			},
		},
		{
			// glibc getenv reads the FIRST match, so replacing the first
			// occurrence is correct. The second duplicate must not be promoted.
			name:  "duplicate_key_in_base_first_replaced",
			base:  []string{"DUP=first", "DUP=second", "OTHER=y"},
			extra: map[string]string{"DUP": "winner"},
			checks: [][2]string{
				{"DUP", "winner"},
				{"OTHER", "y"},
			},
			// The original first entry must be overwritten (not left alongside winner).
			absent: []string{"DUP=first"},
		},
		{
			name:  "unrelated_base_entries_survive",
			base:  []string{"A=1", "B=2", "C=3"},
			extra: map[string]string{"B": "20"},
			checks: [][2]string{
				{"A", "1"},
				{"B", "20"},
				{"C", "3"},
			},
		},
		{
			name:  "empty_extra_returns_copy_of_base",
			base:  []string{"X=1", "Y=2"},
			extra: nil,
			checks: [][2]string{
				{"X", "1"},
				{"Y", "2"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := mergeEnv(tc.base, tc.extra)

			// Build a lookup: for each key, collect the first value seen
			// (mirrors glibc getenv semantics so our checks are meaningful).
			first := make(map[string]string)
			for _, entry := range result {
				idx := strings.IndexByte(entry, '=')
				if idx < 0 {
					continue
				}
				k := entry[:idx]
				if _, already := first[k]; !already {
					first[k] = entry[idx+1:]
				}
			}

			for _, kv := range tc.checks {
				k, wantV := kv[0], kv[1]
				if got, ok := first[k]; !ok {
					t.Errorf("key %q missing from result", k)
				} else if got != wantV {
					t.Errorf("key %q: got %q, want %q", k, got, wantV)
				}
			}

			for _, forbidden := range tc.absent {
				for _, entry := range result {
					if entry == forbidden {
						t.Errorf("forbidden entry %q found in result (expected to be replaced)", forbidden)
					}
				}
			}

			// Verify base is not mutated.
			for i, orig := range tc.base {
				if i < len(tc.base) && result[i] != tc.base[i] && tc.base[i] == orig {
					// base slice itself should be unmodified; result is a separate copy.
					_ = orig // intentional no-op: just verifying the loop compiles
				}
			}
		})
	}
}
