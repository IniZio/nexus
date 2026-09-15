package cli

import (
	"strings"
	"testing"
)

func TestClaudeReadyMatch_AutoModeFooter(t *testing.T) {
	/** T0a evidence: auto-mode footer is "⏵⏵ auto mode on (shift+tab to cycle)";
	discriminator must be "auto mode on" (substring match, herdr pane wait-output).
	Mutation: claudeReadyMatch(true) returns OLD bypass value "shift+tab to cycle"
	→ autoModeTranscript test FAILS. Bypass footer "shift+tab to cycle (bypass mode)"
	→ must NOT match (distinguishes modes). */
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
	const autoModeFooter = "⏵⏵ auto mode on (shift+tab to cycle)"
	discriminator := claudeReadyMatch(true)
	if !strings.Contains(autoModeFooter, discriminator) {
		t.Errorf("auto-mode footer %q does not contain discriminator %q", autoModeFooter, discriminator)
	}
}

func TestClaudeReadyMatch_DoesNotMatchBypassFooter(t *testing.T) {
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
