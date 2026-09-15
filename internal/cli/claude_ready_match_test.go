package cli

import (
	"strings"
	"testing"
)

func TestClaudeReadyMatch_AutoModeFooter(t *testing.T) {
	/** T0a evidence: auto-mode footer is "⏵⏵ auto mode on (shift+tab to cycle)";
	discriminator must be "auto mode on" (substring match, herdr pane wait-output).
	Mutation: claudeReadyMatch returns OLD bypass value "shift+tab to cycle"
	→ autoModeTranscript test FAILS. Bypass footer "shift+tab to cycle (bypass mode)"
	→ must NOT match (distinguishes modes). */
	const want = "auto mode on"
	if got := claudeReadyMatch(true); got != want {
		t.Errorf("claudeReadyMatch(true) = %q; want %q", got, want)
	}
	if got := claudeReadyMatch(false); got != want {
		t.Errorf("claudeReadyMatch(false) = %q; want %q", got, want)
	}
}

// TestClaudeReadyMatch_ModeInvariant pins D-2: the guest always runs in auto
// mode, so the readiness token cannot depend on the autonomous flag.
/** Live 2026-09-15: space-agent launched `command claude`, the mounted host
settings.json put it in auto mode, and the "? for shortcuts" wait timed out
against an agent already at its prompt. */
func TestClaudeReadyMatch_ModeInvariant(t *testing.T) {
	const manualFooter = " ⏸ manual mode on · ? for shortcuts · ← for agents"
	for _, autonomous := range []bool{true, false} {
		tok := claudeReadyMatch(autonomous)
		if strings.Contains(manualFooter, tok) {
			t.Errorf("claudeReadyMatch(%v) = %q matches the manual-mode footer; the guest never runs manual", autonomous, tok)
		}
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
