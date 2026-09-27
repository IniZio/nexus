package controller_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/controller"
	"github.com/IniZio/nexus/internal/controller/chattest"
	controllerconfig "github.com/IniZio/nexus/internal/controller/config"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/vault"
	"github.com/IniZio/nexus/internal/core/vault/vaulttest"
)

// pollErrConnector is a device-flow connector whose PollDevice always fails.
type pollErrConnector struct {
	*vaulttest.FakeConnector
	pollErr error
}

func (c *pollErrConnector) PollDevice(_ context.Context, _ string) (vault.Record, error) {
	return vault.Record{}, c.pollErr
}

// errVault wraps a vault.Vault and injects a Put error.
type errVault struct {
	inner  vault.Vault
	putErr error
}

func (v *errVault) Get(ctx context.Context, k vault.Key) (vault.Record, error) {
	return v.inner.Get(ctx, k)
}
func (v *errVault) Put(_ context.Context, _ vault.Key, _ vault.Record) error { return v.putErr }
func (v *errVault) Delete(ctx context.Context, k vault.Key) error            { return v.inner.Delete(ctx, k) }
func (v *errVault) List(ctx context.Context) ([]vault.Key, error)            { return v.inner.List(ctx) }
func (v *errVault) Source(k vault.Key, project string) (cred.CredentialSource, error) {
	return v.inner.Source(k, project)
}
func (v *errVault) ForceRefresh(ctx context.Context, k vault.Key) (vault.Record, error) {
	return v.inner.ForceRefresh(ctx, k)
}

func waitForPost(t *testing.T, ch *chattest.Fake, ref controller.ThreadRef, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, p := range ch.Posts(ref) {
			if strings.Contains(p, want) {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for post containing %q; got %v", want, ch.Posts(ref))
}

func waitForEphemeral(t *testing.T, ch *chattest.Fake, ref controller.ThreadRef, user, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, p := range ch.Ephemerals(ref, user) {
			if strings.Contains(p, want) {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for ephemeral containing %q; got %v", want, ch.Ephemerals(ref, user))
}

func newLinkerWithVault(t *testing.T, v vault.Vault) (*controller.VaultLinker, *chattest.Fake, *vault.Registry) {
	t.Helper()
	return newLinkerWithMode(t, v, controllerconfig.ModeLocal)
}

func newLinkerWithMode(t *testing.T, v vault.Vault, mode controllerconfig.DeploymentMode) (*controller.VaultLinker, *chattest.Fake, *vault.Registry) {
	t.Helper()
	ch := chattest.New()
	reg := vault.NewRegistry()
	return controller.NewVaultLinker(v, reg, ch, "T1", mode), ch, reg
}

// syncConnector wraps FakeConnector and signals when PollDevice is done.
type syncConnector struct {
	*vaulttest.FakeConnector
	done chan struct{}
	mu   sync.Once
}

func (c *syncConnector) PollDevice(ctx context.Context, code string) (vault.Record, error) {
	rec, err := c.FakeConnector.PollDevice(ctx, code)
	c.mu.Do(func() { close(c.done) })
	return rec, err
}

func newLinkerSetup(t *testing.T) (*controller.VaultLinker, *chattest.Fake, vault.Vault, *vault.Registry) {
	t.Helper()
	ch := chattest.New()
	v := vaulttest.NewFake()
	reg := vault.NewRegistry()
	return controller.NewVaultLinker(v, reg, ch, "T1", controllerconfig.ModeLocal), ch, v, reg
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

	ephemerals := ch.Ephemerals(ref, "U1")
	if len(ephemerals) == 0 {
		t.Fatalf("no ephemeral messages after /link github; public posts=%v", ch.Posts(ref))
	}
	found := false
	for _, p := range ephemerals {
		if len(p) > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("device code not posted ephemerally; ephemerals=%v", ephemerals)
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
	if len(ch.Ephemerals(ref, "U1")) == 0 {
		t.Fatal("expected ephemeral auth URL post")
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

func TestLinkGitHubPollErrorNotifiesUser(t *testing.T) {
	ctx := context.Background()
	v := vaulttest.NewFake()
	linker, ch, reg := newLinkerWithVault(t, v)

	base := vaulttest.NewFakeConnector("github")
	fc := &pollErrConnector{FakeConnector: base, pollErr: errors.New("authorization_expired")}
	if err := reg.Register(fc); err != nil {
		t.Fatalf("Register: %v", err)
	}

	ref := controller.NewThreadRef("T1", "C1", "ts-poll-err")
	ev := controller.Event{Kind: controller.EventSlashCommand, ThreadRef: ref, User: "U1", Text: "/link github"}
	handler := linker.CommandHandler()
	if err := handler(ctx, ev); err != nil {
		t.Fatalf("CommandHandler: %v", err)
	}

	waitForEphemeral(t, ch, ref, "U1", "github link failed", 3*time.Second)
}

func TestLinkGitHubVaultPutErrorNotifiesUser(t *testing.T) {
	ctx := context.Background()
	inner := vaulttest.NewFake()
	ev := &errVault{inner: inner, putErr: errors.New("disk full")}
	linker, ch, reg := newLinkerWithVault(t, ev)

	fc := vaulttest.NewFakeConnector("github")
	if err := reg.Register(fc); err != nil {
		t.Fatalf("Register: %v", err)
	}

	ref := controller.NewThreadRef("T1", "C1", "ts-put-err")
	event := controller.Event{Kind: controller.EventSlashCommand, ThreadRef: ref, User: "U2", Text: "/link github"}
	handler := linker.CommandHandler()
	if err := handler(ctx, event); err != nil {
		t.Fatalf("CommandHandler: %v", err)
	}

	waitForEphemeral(t, ch, ref, "U2", "github link failed", 3*time.Second)
}

func TestLinkGitHubSuccessPostsConfirmation(t *testing.T) {
	ctx := context.Background()
	inner := vaulttest.NewFake()
	linker, ch, reg := newLinkerWithVault(t, inner)

	done := make(chan struct{})
	fc := &syncConnector{FakeConnector: vaulttest.NewFakeConnector("github"), done: done}
	if err := reg.Register(fc); err != nil {
		t.Fatalf("Register: %v", err)
	}

	ref := controller.NewThreadRef("T1", "C1", "ts-success")
	ev := controller.Event{Kind: controller.EventSlashCommand, ThreadRef: ref, User: "U3", Text: "/link github"}
	handler := linker.CommandHandler()
	if err := handler(ctx, ev); err != nil {
		t.Fatalf("CommandHandler: %v", err)
	}

	waitForEphemeral(t, ch, ref, "U3", "github linked", 3*time.Second)
}

func TestLinkGitHubLaptopModeAllowsStar(t *testing.T) {
	ctx := context.Background()
	inner := vaulttest.NewFake()
	linker, ch, reg := newLinkerWithMode(t, inner, controllerconfig.ModeLocal)

	done := make(chan struct{})
	fc := &syncConnector{FakeConnector: vaulttest.NewFakeConnector("github"), done: done}
	if err := reg.Register(fc); err != nil {
		t.Fatalf("Register: %v", err)
	}

	ref := controller.NewThreadRef("T1", "C1", "ts-laptop")
	ev := controller.Event{Kind: controller.EventSlashCommand, ThreadRef: ref, User: "U4", Text: "/link github"}
	if err := linker.CommandHandler()(ctx, ev); err != nil {
		t.Fatalf("CommandHandler: %v", err)
	}

	waitForEphemeral(t, ch, ref, "U4", "github linked", 3*time.Second)

	k := vault.Key{Principal: vault.SlackPrincipal("T1", "U4"), Integration: "github"}
	rec, err := inner.Get(ctx, k)
	if err != nil {
		t.Fatalf("vault.Get: %v", err)
	}
	if len(rec.AllowedProjects) != 1 || rec.AllowedProjects[0] != "*" {
		t.Fatalf("AllowedProjects = %v, want [\"*\"]", rec.AllowedProjects)
	}
}

func TestLinkGitHubSharedModeNoProjects(t *testing.T) {
	ctx := context.Background()
	inner := vaulttest.NewFake()
	linker, ch, reg := newLinkerWithMode(t, inner, controllerconfig.ModeShared)

	done := make(chan struct{})
	fc := &syncConnector{FakeConnector: vaulttest.NewFakeConnector("github"), done: done}
	if err := reg.Register(fc); err != nil {
		t.Fatalf("Register: %v", err)
	}

	ref := controller.NewThreadRef("T1", "C1", "ts-shared")
	ev := controller.Event{Kind: controller.EventSlashCommand, ThreadRef: ref, User: "U5", Text: "/link github"}
	if err := linker.CommandHandler()(ctx, ev); err != nil {
		t.Fatalf("CommandHandler: %v", err)
	}

	waitForEphemeral(t, ch, ref, "U5", "no projects", 3*time.Second)

	k := vault.Key{Principal: vault.SlackPrincipal("T1", "U5"), Integration: "github"}
	rec, err := inner.Get(ctx, k)
	if err != nil {
		t.Fatalf("vault.Get: %v", err)
	}
	if len(rec.AllowedProjects) != 0 {
		t.Fatalf("AllowedProjects = %v, want empty", rec.AllowedProjects)
	}
}
