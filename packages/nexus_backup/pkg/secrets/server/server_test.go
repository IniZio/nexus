package server

import (
	"testing"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/secrets/discovery"
	"github.com/inizio/nexus/packages/nexus/pkg/secrets/vending"
	"github.com/inizio/nexus/packages/nexus/pkg/secrets/vsock"
)

func TestVendingServerReturnsToken(t *testing.T) {
	configs := []discovery.ProviderConfig{
		{Name: "test", Type: discovery.ProviderTypeAPIKey, AccessToken: "api_key_123"},
	}

	svc := vending.NewService(configs)
	vendServer := New(svc, 10792)

	if err := vendServer.Start(); err != nil {
		t.Fatal(err)
	}
	defer vendServer.Stop()

	time.Sleep(100 * time.Millisecond)

	client := vsock.NewClient(10792)
	resp, err := client.RequestToken("ws-1", "test")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if resp.Token != "api_key_123" {
		t.Errorf("expected 'api_key_123', got '%s'", resp.Token)
	}
}
