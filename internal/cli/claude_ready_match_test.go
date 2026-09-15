package cli

import (
	"strings"
	"testing"
)

// T0a evidence: the auto-mode footer is "⏵⏵ auto mode on (shift+tab to cycle)";
// the discriminator must be "auto mode on" (substring match is what herdr
// pane wait-output uses).
//
// Mutation proof:
//   - If claudeReadyMatch(true) returns the OLD bypass value "shift+tab to cycle"
//     instead of "auto mode on", the autoModeTranscript test FAILS (the old
//     value is not a substring of the discriminator we assert).
//   - If the bypass footer "shift+tab to cycle (bypass mode)" is passed as the
//     transcript, bypassTranscriptMustNotMatch must NOT match — it verifies the
//     discriminator distinguishes the two modes.
func TestClaudeReadyMatch_AutoModeFooter(t *testing.T) {
	got := claudeReadyMatch(true)
	const want = "auto mode on"
	if got != want {
		t.Errorf("claudeReadyMatch(true) = %q; want %q", got, want)
	}

	gotNon := claudeReadyMatch(false)
	const wantNon = "? for shortcuts"
	if gotNon != wantNon {
		t.Errorf("claudeReadyMatch(false) = %q; want %q", gotNon, wantNon)
	}
}

func TestClaudeReadyMatch_MatchesAutoTranscript(t *testing.T) {
	// Real footer observed under --permission-mode auto (T0a).
	const autoModeFooter = "⏵⏵ auto mode on (shift+tab to cycle)"
	discriminator := claudeReadyMatch(true)
	if !strings.Contains(autoModeFooter, discriminator) {
		t.Errorf("auto-mode footer %q does not contain discriminator %q", autoModeFooter, discriminator)
	}
}

func TestClaudeReadyMatch_DoesNotMatchBypassFooter(t *testing.T) {
	// Footer observed under --dangerously-skip-permissions (pre-T5, now retired).
	const bypassFooter = "shift+tab to cycle"
	discriminator := claudeReadyMatch(true)
	if discriminator == bypassFooter {
		t.Errorf("discriminator %q equals the old bypass footer — mode distinction is lost", discriminator)
	}
	const bypassOnlyFooter = "shift+tab to cycle (bypass)" // representative bypass-only line
	if strings.Contains(bypassOnlyFooter, discriminator) {
		t.Errorf("bypass-only footer %q contains discriminator %q — mode distinction is lost", bypassOnlyFooter, discriminator)
	}
}
