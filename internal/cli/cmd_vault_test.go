package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/vault"
)

func TestVaultLinkUnknownConnectorFails(t *testing.T) {
	var buf bytes.Buffer
	out := NewOutput(&buf, &buf, false)
	err := runVault(context.Background(), []string{"link", "notanintegration"}, out)
	if err == nil {
		t.Fatal("expected error for unknown integration; got nil")
	}
	if _, ok := err.(*UsageError); !ok {
		t.Errorf("expected UsageError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "notanintegration") {
		t.Errorf("error should name the unknown integration; got: %v", err)
	}
}

func TestVaultLsHidesTokens(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_DATA_HOME", dir)

	st, err := vaultStore()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	k := vault.Key{Principal: "local:testuser", Integration: "github"}
	rec := vault.Record{
		AccessToken:     "tok-secret-do-not-print",
		Expiry:          time.Now().Add(time.Hour),
		AllowedProjects: []string{"*"},
	}
	if err := st.Put(ctx, k, rec); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	out := NewOutput(&buf, &buf, false)
	if err := runVaultLs(ctx, nil, out); err != nil {
		t.Fatalf("vault ls: %v", err)
	}
	got := buf.String()
	if strings.Contains(got, "tok-secret") {
		t.Errorf("vault ls must not print access token; output: %q", got)
	}
	if !strings.Contains(got, "github") {
		t.Errorf("vault ls should show the integration name; output: %q", got)
	}
}
