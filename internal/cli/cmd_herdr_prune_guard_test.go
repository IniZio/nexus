package cli

import (
	"strings"
	"testing"
)

func TestHerdrGlobalPruneGuard(t *testing.T) {
	const def = "/home/user/.config/herdr/sessions/default/herdr.sock"
	const alt = "/home/user/.config/herdr/sessions/debug/herdr.sock"
	const other = "/home/user/.config/herdr/sessions/other/herdr.sock"

	own := HerdrSpaceBinding{HerdrSession: def}
	legacy := HerdrSpaceBinding{HerdrSession: ""}
	foreign := HerdrSpaceBinding{HerdrSession: alt}
	foreignOther := HerdrSpaceBinding{HerdrSession: other}

	tests := []struct {
		name         string
		cur          string
		def          string
		bindings     []HerdrSpaceBinding
		allowForeign bool
		wantErr      bool
		wantContains []string
		wantAbsent   []string
	}{
		{
			name:     "default session, own+legacy bindings → nil",
			cur:      def,
			def:      def,
			bindings: []HerdrSpaceBinding{own, legacy},
			wantErr:  false,
		},
		{
			name:     "default session, no bindings → nil",
			cur:      def,
			def:      def,
			bindings: nil,
			wantErr:  false,
		},
		{
			name:         "non-default session → refuse, mentions socket and flags",
			cur:          alt,
			def:          def,
			bindings:     nil,
			wantErr:      true,
			wantContains: []string{alt, "--allow-foreign-sessions", "--workspace"},
		},
		{
			name:         "foreign binding → refuse, lists session",
			cur:          def,
			def:          def,
			bindings:     []HerdrSpaceBinding{own, legacy, foreign},
			wantErr:      true,
			wantContains: []string{alt, "--allow-foreign-sessions", "--workspace", "1 binding(s)"},
		},
		{
			name:         "multiple foreign sessions → listed sorted",
			cur:          def,
			def:          def,
			bindings:     []HerdrSpaceBinding{foreign, foreignOther, foreign},
			wantErr:      true,
			wantContains: []string{alt, other},
		},
		{
			name:         "foreign session count aggregated",
			cur:          def,
			def:          def,
			bindings:     []HerdrSpaceBinding{foreign, foreign, foreign},
			wantErr:      true,
			wantContains: []string{"3 binding(s)"},
		},
		{
			name:         "allowForeign overrides non-default session → nil",
			cur:          alt,
			def:          def,
			bindings:     nil,
			allowForeign: true,
			wantErr:      false,
		},
		{
			name:         "allowForeign overrides foreign binding → nil",
			cur:          def,
			def:          def,
			bindings:     []HerdrSpaceBinding{foreign},
			allowForeign: true,
			wantErr:      false,
		},
		{
			name:         "legacy-only bindings → nil (not foreign)",
			cur:          def,
			def:          def,
			bindings:     []HerdrSpaceBinding{legacy, legacy},
			wantErr:      false,
		},
		{
			name:         "error has required prefix",
			cur:          alt,
			def:          def,
			bindings:     nil,
			wantErr:      true,
			wantContains: []string{herdrPruneGuardPrefix},
		},
		{
			name:     "foreign binding dedup: alt appears once in list",
			cur:      def,
			def:      def,
			bindings: []HerdrSpaceBinding{foreign, foreign},
			wantErr:  true,
			// alt should appear once in the session list (not twice)
			wantContains: []string{alt},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := herdrGlobalPruneGuard(tc.cur, tc.def, tc.bindings, tc.allowForeign)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected nil, got %v", err)
			}
			if err != nil {
				msg := err.Error()
				for _, want := range tc.wantContains {
					if !strings.Contains(msg, want) {
						t.Errorf("error message missing %q:\n%s", want, msg)
					}
				}
				for _, absent := range tc.wantAbsent {
					if strings.Contains(msg, absent) {
						t.Errorf("error message should not contain %q:\n%s", absent, msg)
					}
				}
				// Verify UsageError type.
				if _, ok := err.(*UsageError); !ok {
					t.Errorf("expected *UsageError, got %T", err)
				}
			}
		})
	}
}

// TestHerdrGlobalPruneGuardCapAt5 verifies that at most 5 sessions are listed.
func TestHerdrGlobalPruneGuardCapAt5(t *testing.T) {
	const def = "/run/herdr/default.sock"
	bindings := make([]HerdrSpaceBinding, 8)
	for i := range bindings {
		bindings[i] = HerdrSpaceBinding{HerdrSession: "/run/herdr/sess" + string(rune('a'+i)) + ".sock"}
	}
	err := herdrGlobalPruneGuard(def, def, bindings, false)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "3 more session(s)") {
		t.Errorf("expected overflow message; got:\n%s", msg)
	}
}
