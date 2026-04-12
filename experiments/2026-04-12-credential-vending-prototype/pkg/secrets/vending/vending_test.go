package vending

import (
	"context"
	"testing"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/secrets/discovery"
)

func TestServiceListProviders(t *testing.T) {
	configs := []discovery.ProviderConfig{
		{Name: "codex", Type: discovery.ProviderTypeOAuth, AccessToken: "test"},
		{Name: "opencode", Type: discovery.ProviderTypeAPIKey, AccessToken: "test"},
	}

	svc := NewService(configs)
	providers := svc.ListProviders()

	if len(providers) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(providers))
	}

	found := make(map[string]bool)
	for _, p := range providers {
		found[p] = true
	}

	if !found["codex"] {
		t.Error("codex not in provider list")
	}
	if !found["opencode"] {
		t.Error("opencode not in provider list")
	}
}

func TestServiceGetTokenAPIKey(t *testing.T) {
	configs := []discovery.ProviderConfig{
		{Name: "opencode", Type: discovery.ProviderTypeAPIKey, AccessToken: "oc_key_12345"},
	}

	svc := NewService(configs)
	ctx := context.Background()

	token, err := svc.GetToken(ctx, "opencode")
	if err != nil {
		t.Fatalf("failed to get token: %v", err)
	}

	if token.Value != "oc_key_12345" {
		t.Errorf("expected token 'oc_key_12345', got '%s'", token.Value)
	}

	if token.Provider != "opencode" {
		t.Errorf("expected provider 'opencode', got '%s'", token.Provider)
	}

	// API key tokens should have long expiry
	if token.IsExpired() {
		t.Error("API key token should not be expired immediately")
	}
}

func TestServiceGetTokenUnknownProvider(t *testing.T) {
	svc := NewService([]discovery.ProviderConfig{})
	ctx := context.Background()

	_, err := svc.GetToken(ctx, "unknown")
	if err == nil {
		t.Error("expected error for unknown provider")
	}

	if err.Error() != "provider unknown not configured" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestTokenExpiration(t *testing.T) {
	// Create an already-expired token
	token := &Token{
		Value:     "expired",
		ExpiresAt: time.Now().Add(-5 * time.Minute),
		Provider:  "test",
	}

	if !token.IsExpired() {
		t.Error("token should be expired")
	}

	// Create a fresh token
	freshToken := &Token{
		Value:     "fresh",
		ExpiresAt: time.Now().Add(5 * time.Minute),
		Provider:  "test",
	}

	if freshToken.IsExpired() {
		t.Error("fresh token should not be expired")
	}

	// Token within 60s buffer should be considered expired
	bufferToken := &Token{
		Value:     "buffer",
		ExpiresAt: time.Now().Add(30 * time.Second),
		Provider:  "test",
	}

	if !bufferToken.IsExpired() {
		t.Error("token within 60s buffer should be considered expired")
	}
}

func TestStaticBroker(t *testing.T) {
	broker := &staticBroker{
		provider: "test",
		token: &Token{
			Value:     "static_token",
			ExpiresAt: time.Now().Add(1 * time.Hour),
			Provider:  "test",
		},
	}

	if broker.Name() != "test" {
		t.Errorf("expected name 'test', got '%s'", broker.Name())
	}

	ctx := context.Background()
	token, err := broker.GetToken(ctx)
	if err != nil {
		t.Fatalf("failed to get token: %v", err)
	}

	if token.Value != "static_token" {
		t.Errorf("expected 'static_token', got '%s'", token.Value)
	}

	// Test expired token
	expiredBroker := &staticBroker{
		provider: "expired",
		token: &Token{
			Value:     "expired_token",
			ExpiresAt: time.Now().Add(-1 * time.Hour),
			Provider:  "expired",
		},
	}

	_, err = expiredBroker.GetToken(ctx)
	if err == nil {
		t.Error("expected error for expired token")
	}
}
