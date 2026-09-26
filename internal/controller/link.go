package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/IniZio/nexus/internal/core/vault"
	"github.com/IniZio/nexus/internal/core/vault/linkflow"
)

type pkceState struct {
	state        string
	codeVerifier string
}

// VaultLinker implements Linker backed by a vault.Vault.
type VaultLinker struct {
	v         vault.Vault
	reg       *vault.Registry
	chat      ChatAdapter
	team      string
	GenState  func() string
	mu        sync.Mutex
	pkceStates map[string]pkceState
}

func NewVaultLinker(v vault.Vault, reg *vault.Registry, chat ChatAdapter, team string) *VaultLinker {
	return &VaultLinker{
		v:          v,
		reg:        reg,
		chat:       chat,
		team:       team,
		GenState:   randomState,
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
	if err := l.chat.Post(ctx, ev.ThreadRef, msg); err != nil {
		return err
	}
	go l.pollGitHub(context.Background(), c, da.DeviceCode, ev.User)
	return nil
}

func (l *VaultLinker) pollGitHub(ctx context.Context, c vault.Connector, deviceCode, user string) {
	rec, err := c.PollDevice(ctx, deviceCode)
	if err != nil {
		return
	}
	k := vault.Key{Principal: vault.SlackPrincipal(l.team, user), Integration: "github"}
	_ = l.v.Put(ctx, k, rec)
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
	return l.chat.Post(ctx, ev.ThreadRef, "Authorize Linear: "+authURL)
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
	return l.chat.Post(ctx, ev.ThreadRef, "Linear linked!")
}

func randomState() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
