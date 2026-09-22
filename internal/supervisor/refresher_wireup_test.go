package supervisor

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/perimeter/cred"
)

func TestBuildClaudeRefreshers_WithStore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	storePath := filepath.Join(dir, "creds.json")
	store := &cred.DedicatedCredStore{
		AccessToken:   "fake-access-token",
		RefreshToken:  "fake-refresh-token",
		ExpiresAt:     time.Now().Add(time.Hour),
		TokenType:     "Bearer",
		TokenEndpoint: "https://platform.claude.com/v1/oauth/token",
	}
	if err := cred.SaveStore(storePath, store); err != nil {
		t.Fatalf("SaveStore: %v", err)
	}
	broker := cred.NewBroker()
	rs := buildClaudeRefreshers(storePath, broker)
	if len(rs) == 0 {
		t.Fatal("expected at least one Refresher, got 0")
	}
	found := false
	for _, r := range rs {
		if r.Host() == "api.anthropic.com" {
			found = true
		}
	}
	if !found {
		t.Errorf("no Refresher for api.anthropic.com; got hosts: %v", refresherHosts(rs))
	}
}

func TestBuildClaudeRefreshers_AbsentStore(t *testing.T) {
	t.Parallel()
	broker := cred.NewBroker()
	rs := buildClaudeRefreshers("/nonexistent/path/creds.json", broker)
	if len(rs) != 0 {
		t.Errorf("expected 0 Refreshers for absent store, got %d", len(rs))
	}
}

func TestBuildClaudeRefreshers_EmptyPath(t *testing.T) {
	t.Parallel()
	broker := cred.NewBroker()
	rs := buildClaudeRefreshers("", broker)
	if len(rs) != 0 {
		t.Errorf("expected 0 Refreshers for empty path, got %d", len(rs))
	}
}

func refresherHosts(rs []*cred.Refresher) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Host()
	}
	return out
}
