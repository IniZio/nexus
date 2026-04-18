package credentials

import (
	"context"
	"testing"

	authrelay "github.com/inizio/nexus/packages/nexus/pkg/infra/relay"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

func TestHandleAuthRelayMint(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())
	ws, err := mgr.Create(context.Background(), workspacemgr.CreateSpec{
		Repo:          "git@example/repo.git",
		WorkspaceName: "alpha",
		AgentProfile:  "default",
		AuthBinding: map[string]string{
			"claude": "token-123",
		},
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	broker := authrelay.NewBroker()
	res, rpcErr := HandleAuthRelayMint(context.Background(), AuthRelayMintParams{
		WorkspaceID: ws.ID,
		Binding:     "claude",
	}, mgr, broker)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if res.Token == "" {
		t.Fatal("expected non-empty relay token")
	}

	env, ok := broker.Consume(res.Token, ws.ID)
	if !ok {
		t.Fatal("expected minted token to be consumable")
	}
	if env["NEXUS_AUTH_BINDING"] != "claude" {
		t.Fatalf("expected binding env claude, got %q", env["NEXUS_AUTH_BINDING"])
	}
	if env["NEXUS_AUTH_VALUE"] != "token-123" {
		t.Fatalf("expected injected value token-123, got %q", env["NEXUS_AUTH_VALUE"])
	}
	if env["ANTHROPIC_API_KEY"] != "token-123" {
		t.Fatalf("expected ANTHROPIC_API_KEY token-123, got %q", env["ANTHROPIC_API_KEY"])
	}
}

func TestHandleAuthRelayRevoke(t *testing.T) {
	broker := authrelay.NewBroker()
	token := broker.Mint("ws-1", map[string]string{"NEXUS_AUTH_VALUE": "abc"}, 0)

	res, rpcErr := HandleAuthRelayRevoke(context.Background(), AuthRelayRevokeParams{Token: token}, broker)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if !res.Revoked {
		t.Fatal("expected revoked=true")
	}

	if _, ok := broker.Consume(token, "ws-1"); ok {
		t.Fatal("expected revoked token to be unusable")
	}
}
