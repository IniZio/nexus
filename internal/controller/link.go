package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	controllerconfig "github.com/IniZio/nexus/internal/controller/config"
	"github.com/IniZio/nexus/internal/core/vault"
	"github.com/IniZio/nexus/internal/core/vault/connectors"
	"github.com/IniZio/nexus/internal/core/vault/linkflow"
)

type pkceState struct {
	state        string
	codeVerifier string
}

// VaultLinker implements Linker backed by a vault.Vault.
type VaultLinker struct {
	v          vault.Vault
	reg        *vault.Registry
	chat       ChatAdapter
	team       string
	mode       controllerconfig.DeploymentMode
	GenState   func() string
	PollSleep  func(time.Duration)
	mu         sync.Mutex
	pkceStates map[string]pkceState
}

func NewVaultLinker(v vault.Vault, reg *vault.Registry, chat ChatAdapter, team string, mode controllerconfig.DeploymentMode) *VaultLinker {
	if mode == "" {
		mode = controllerconfig.ModeLocal
	}
	return &VaultLinker{
		v:          v,
		reg:        reg,
		chat:       chat,
		team:       team,
		mode:       mode,
		GenState:   randomState,
		PollSleep:  time.Sleep,
		pkceStates: make(map[string]pkceState),
	}
}

func (l *VaultLinker) Require(ctx context.Context, user string) error {
	k := vault.Key{Principal: vault.SlackPrincipal(l.team, user), Integration: "github"}
	_, err := l.v.Get(ctx, k)
	if errors.Is(err, vault.ErrUnlinked) {
		return fmt.Errorf("%w: link with /link github", ErrNotLinked)
	}
	return err
}

func (l *VaultLinker) StartLink(ctx context.Context, user, integration string) (string, error) {
	c, err := l.reg.Get(integration)
	if err != nil {
		return "", err
	}
	switch c.LinkFlow() {
	case vault.LinkFlowDevice:
		da, dErr := c.StartDevice(ctx)
		if dErr != nil {
			return "", dErr
		}
		return fmt.Sprintf("Enter code: %s at %s", da.UserCode, da.VerificationURI), nil
	case vault.LinkFlowPKCE:
		state := l.GenState()
		authURL, codeVerifier, aErr := c.AuthURL(state)
		if aErr != nil {
			return "", aErr
		}
		l.mu.Lock()
		l.pkceStates[user] = pkceState{state: state, codeVerifier: codeVerifier}
		l.mu.Unlock()
		return authURL, nil
	default:
		return "", ErrNotImplemented
	}
}

func (l *VaultLinker) CommandHandler() Handler {
	return func(ctx context.Context, ev Event) error {
		return l.handleCommand(ctx, ev)
	}
}

func (l *VaultLinker) handleCommand(ctx context.Context, ev Event) error {
	text := strings.TrimSpace(ev.Text)
	text = strings.TrimPrefix(text, "/link")
	text = strings.TrimSpace(text)
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return nil
	}
	switch parts[0] {
	case "github":
		return l.linkGitHub(ctx, ev)
	case "linear":
		if len(parts) > 1 {
			return l.linkLinearPaste(ctx, ev, strings.Join(parts[1:], " "))
		}
		return l.linkLinearStart(ctx, ev)
	}
	return nil
}

func (l *VaultLinker) linkGitHub(ctx context.Context, ev Event) error {
	c, err := l.reg.Get("github")
	if err != nil {
		return err
	}
	da, err := c.StartDevice(ctx)
	if err != nil {
		return err
	}
	msg := fmt.Sprintf("Enter code *%s* at: %s", da.UserCode, da.VerificationURI)
	if err := l.chat.PostEphemeral(ctx, ev.ThreadRef, ev.User, msg); err != nil {
		return err
	}
	expiry := 300 * time.Second
	if da.ExpiresIn > 0 {
		expiry = time.Duration(da.ExpiresIn) * time.Second
	}
	pollCtx, cancel := context.WithTimeout(context.Background(), expiry)
	go func() {
		defer cancel()
		l.pollGitHub(pollCtx, c, da, ev.User, ev.ThreadRef)
	}()
	return nil
}

func (l *VaultLinker) pollGitHub(ctx context.Context, c vault.Connector, da vault.DeviceAuth, user string, ref ThreadRef) {
	rec, err := connectors.WaitDevice(ctx, c, da, l.PollSleep)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			_ = l.chat.PostEphemeral(ctx, ref, user, "github link expired, run /link github again")
			return
		}
		slog.Error("github device poll failed", "user", user, "err", err)
		_ = l.chat.PostEphemeral(ctx, ref, user, fmt.Sprintf("github link failed: %v", err))
		return
	}
	// Connectors no longer set policy; controller sets AllowedProjects per deployment mode.
	if l.mode == controllerconfig.ModeShared {
		rec.AllowedProjects = nil
	} else {
		rec.AllowedProjects = []string{"*"}
	}
	k := vault.Key{Principal: vault.SlackPrincipal(l.team, user), Integration: "github"}
	if err := l.v.Put(ctx, k, rec); err != nil {
		slog.Error("github vault put failed", "user", user, "err", err)
		_ = l.chat.PostEphemeral(ctx, ref, user, fmt.Sprintf("github link failed: %v", err))
		return
	}
	slog.Info("github linked", "user", user)
	var msg string
	if l.mode == controllerconfig.ModeShared {
		msg = "github linked, but no projects are allowed yet — ask the controller operator to grant access"
	} else {
		msg = "github linked"
	}
	_ = l.chat.PostEphemeral(ctx, ref, user, msg)
}

func (l *VaultLinker) linkLinearStart(ctx context.Context, ev Event) error {
	c, err := l.reg.Get("linear")
	if err != nil {
		return err
	}
	state := l.GenState()
	authURL, codeVerifier, err := c.AuthURL(state)
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.pkceStates[ev.User] = pkceState{state: state, codeVerifier: codeVerifier}
	l.mu.Unlock()
	return l.chat.PostEphemeral(ctx, ev.ThreadRef, ev.User, "Authorize Linear: "+authURL)
}

func (l *VaultLinker) linkLinearPaste(ctx context.Context, ev Event, rawURL string) error {
	l.mu.Lock()
	ps, ok := l.pkceStates[ev.User]
	l.mu.Unlock()
	if !ok {
		return fmt.Errorf("controller: no pending linear link; run /link linear first")
	}

	code, err := linkflow.ExtractCode(rawURL, ps.state)
	if err != nil {
		return err
	}

	c, err := l.reg.Get("linear")
	if err != nil {
		return err
	}

	rec, err := c.Exchange(ctx, code, ps.codeVerifier)
	if err != nil {
		return err
	}

	k := vault.Key{Principal: vault.SlackPrincipal(l.team, ev.User), Integration: "linear"}
	if err := l.v.Put(ctx, k, rec); err != nil {
		return err
	}

	l.mu.Lock()
	delete(l.pkceStates, ev.User)
	l.mu.Unlock()
	return l.chat.PostEphemeral(ctx, ev.ThreadRef, ev.User, "Linear linked!")
}

func randomState() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("controller: crypto/rand: %v", err))
	}
	return hex.EncodeToString(b)
}
