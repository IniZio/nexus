package controller_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/IniZio/nexus/internal/controller"
	"github.com/IniZio/nexus/internal/controller/chattest"
	"github.com/IniZio/nexus/internal/core/vault"
	"github.com/IniZio/nexus/internal/core/vault/vaulttest"
)

func newLinkerSetup(t *testing.T) (*controller.VaultLinker, *chattest.Fake, vault.Vault, *vault.Registry) {
	t.Helper()
	ch := chattest.New()
	v := vaulttest.NewFake()
	reg := vault.NewRegistry()
	return controller.NewVaultLinker(v, reg, ch, "T1"), ch, v, reg
}

func TestRequireRefusesUnlinkedPrincipal(t *testing.T) {
	ctx := context.Background()
	linker, _, _, _ := newLinkerSetup(t)

	err := linker.Require(ctx, "U1")
	if err == nil {
		t.Fatal("expected error; got nil")
	}
	if !errors.Is(err, controller.ErrNotLinked) {
		t.Fatalf("err=%v; want ErrNotLinked", err)
	}
}

func TestLinkGitHubPostsDeviceCodeEphemeral(t *testing.T) {
	ctx := context.Background()
	linker, ch, _, reg := newLinkerSetup(t)

	fc := vaulttest.NewFakeConnector("github")
	if err := reg.Register(fc); err != nil {
		t.Fatalf("Register: %v", err)
	}

	ref := controller.NewThreadRef("T1", "C1", "ts1")
	ev := controller.Event{Kind: controller.EventSlashCommand, ThreadRef: ref, User: "U1", Text: "/link github"}
	handler := linker.CommandHandler()
	if err := handler(ctx, ev); err != nil {
		t.Fatalf("CommandHandler: %v", err)
	}

	posts := ch.Posts(ref)
	if len(posts) == 0 {
		t.Fatal("no posts after /link github")
	}
	found := false
	for _, p := range posts {
		if len(p) > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("device code not posted; posts=%v", posts)
	}
}

func TestLinkLinearPasteURLCompletes(t *testing.T) {
	ctx := context.Background()
	linker, ch, v, reg := newLinkerSetup(t)

	fc := vaulttest.NewFakeConnector("linear")
	if err := reg.Register(fc); err != nil {
		t.Fatalf("Register: %v", err)
	}
	fixedState := "fixed-state-123"
	linker.GenState = func() string { return fixedState }

	ref := controller.NewThreadRef("T1", "C1", "ts1")
	ev := controller.Event{Kind: controller.EventSlashCommand, ThreadRef: ref, User: "U1", Text: "/link linear"}
	handler := linker.CommandHandler()
	if err := handler(ctx, ev); err != nil {
		t.Fatalf("CommandHandler /link linear: %v", err)
	}
	if len(ch.Posts(ref)) == 0 {
		t.Fatal("expected auth URL post")
	}

	pastedURL := fmt.Sprintf("https://example.com/callback?code=abc123&state=%s", fixedState)
	ev2 := controller.Event{Kind: controller.EventSlashCommand, ThreadRef: ref, User: "U1", Text: "/link linear " + pastedURL}
	if err := handler(ctx, ev2); err != nil {
		t.Fatalf("CommandHandler /link linear <url>: %v", err)
	}

	k := vault.Key{Principal: vault.SlackPrincipal("T1", "U1"), Integration: "linear"}
	rec, err := v.Get(ctx, k)
	if err != nil {
		t.Fatalf("vault.Get after link: %v", err)
	}
	if rec.AccessToken == "" {
		t.Fatal("expected non-empty access token in vault")
	}
}
