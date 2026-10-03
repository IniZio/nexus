package cli

import (
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/config"
)

func TestSpritesSecretNames(t *testing.T) {
	got, err := spritesSecretNames(config.Config{}, sandboxCreateFlags{
		secrets: []string{"GITHUB_TOKEN@github.com"}, agentName: "claude-code",
	})
	if err != nil || strings.Join(got, ",") != "CLAUDE_CODE_OAUTH_TOKEN,GH_TOKEN" {
		t.Fatalf("got %v, %v", got, err)
	}
	if got, err = spritesSecretNames(config.Config{}, sandboxCreateFlags{}); err != nil || len(got) != 0 {
		t.Fatalf("no request: %v, %v", got, err)
	}
	_, err = spritesSecretNames(config.Config{}, sandboxCreateFlags{secrets: []string{"STRIPE_KEY@api.stripe.com"}})
	if err == nil || !strings.Contains(err.Error(), "secret kind STRIPE_KEY unsupported on tier A") {
		t.Fatalf("err = %v", err)
	}
}
