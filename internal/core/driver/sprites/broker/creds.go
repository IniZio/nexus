// Package broker holds the host-side credential stack for one sprite sandbox.
package broker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/perimeter/mitm"
)

// Secret is one credential held host-side. Value never leaves this package
// except through the swap performed by the MITM proxy.
type Secret struct {
	Name  string   // env var name in the sprite, e.g. GH_TOKEN
	Value string   // real token
	Hosts []string // hosts the token may be presented to; first is primary
	// GitHubRepo ("owner/name") pins GitHub hosts to one repository.
	// Required when any host is a GitHub host; otherwise New fails closed.
	GitHubRepo string
}

// CredsConfig configures [NewCreds].
type CredsConfig struct {
	SandboxID domain.SandboxID
	Secrets   []Secret
	// CACertPEM/CAKeyPEM optionally seed the CA (both or neither).
	CACertPEM, CAKeyPEM []byte
	// RefreshStore, when set, is a dedicated OAuth store path. A refresher is
	// built per host of the secret named RefreshSecret, with the broker as the
	// SetRealToken setter.
	RefreshStore  string
	RefreshSecret string
	// Transport overrides the upstream transport (tests).
	Transport *http.Transport
}

// Creds is the assembled stack.
type Creds struct {
	// Handler serves accepted tunnel streams (wrap in http.Server).
	Handler http.Handler
	// Env maps secret name to placeholder; never contains a real token.
	Env map[string]string
	// CACertPEM is the CA certificate for the guest trust store.
	CACertPEM []byte
	// Hosts is the sorted-by-input set of secret hosts the tunnel must carry.
	Hosts []string

	refreshers []*cred.Refresher
}

var githubHosts = map[string]bool{"github.com": true, "api.github.com": true, "uploads.github.com": true}

// NewCreds registers a placeholder per secret, builds the MITM proxy over the
// broker and wires refreshers. It fails closed on an unbound GitHub secret.
func NewCreds(cfg CredsConfig) (*Creds, error) {
	br := cred.NewBroker()
	env := map[string]string{}
	var hosts []string
	seen := map[string]bool{}
	policies := mitm.PathPolicies{}
	var refreshHosts []string

	for _, s := range cfg.Secrets {
		if s.Name == "" || s.Value == "" || len(s.Hosts) == 0 {
			return nil, fmt.Errorf("broker: secret %q needs name, value and hosts", s.Name)
		}
		rec, err := br.RegisterPlaceholder(cfg.SandboxID, s.Hosts[0], s.Value)
		if err != nil {
			return nil, err
		}
		for _, h := range s.Hosts[1:] {
			if err := br.RegisterPlaceholderForHost(cfg.SandboxID, rec.Placeholder, h); err != nil {
				return nil, err
			}
		}
		var gh *mitm.GitHubPolicy
		for _, h := range s.Hosts {
			if !githubHosts[h] {
				continue
			}
			if gh == nil {
				owner, name, ok := strings.Cut(s.GitHubRepo, "/")
				if !ok || owner == "" || name == "" {
					return nil, fmt.Errorf("broker: secret %s on %s requires GitHubRepo owner/name", s.Name, h)
				}
				gh = &mitm.GitHubPolicy{Owner: owner, Name: name}
			}
			if policies[rec.Placeholder] == nil {
				policies[rec.Placeholder] = map[string]mitm.HostPolicy{}
			}
			policies[rec.Placeholder][h] = mitm.HostPolicy{GitHub: gh}
		}
		env[s.Name] = rec.Placeholder
		for _, h := range s.Hosts {
			if !seen[h] {
				seen[h] = true
				hosts = append(hosts, h)
			}
		}
		if cfg.RefreshStore != "" && s.Name == cfg.RefreshSecret {
			refreshHosts = s.Hosts
		}
	}

	proxy, err := mitm.New(mitm.Config{
		SandboxID:     cfg.SandboxID,
		AllowedHosts:  hosts,
		SecretHosts:   hosts,
		Broker:        br,
		PathPolicies:  policies,
		SeedCACertPEM: cfg.CACertPEM,
		SeedCAKeyPEM:  cfg.CAKeyPEM,
		Transport:     cfg.Transport,
	})
	if err != nil {
		return nil, fmt.Errorf("broker: mitm proxy: %w", err)
	}
	caPEM, _, err := proxy.CAKeyPair()
	if err != nil {
		return nil, err
	}

	c := &Creds{Handler: proxy, Env: env, CACertPEM: caPEM, Hosts: hosts}
	for _, h := range refreshHosts {
		r, err := cred.NewRefresher(cfg.RefreshStore, h, br)
		if err != nil {
			return nil, fmt.Errorf("broker: refresher %s: %w", h, err)
		}
		r.Register(cfg.SandboxID)
		c.refreshers = append(c.refreshers, r)
	}
	return c, nil
}

// Refresh vends each refresher's token once, pushing rotations into the
// broker. Errors are joined; the previous real token stays in place on error.
func (c *Creds) Refresh(ctx context.Context) error {
	var errs []error
	for _, r := range c.refreshers {
		if _, _, err := r.Token(ctx); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.Host(), err))
		}
	}
	return errors.Join(errs...)
}

// RunRefresh refreshes every interval until ctx ends.
func (c *Creds) RunRefresh(ctx context.Context, interval time.Duration, onErr func(error)) {
	if len(c.refreshers) == 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := c.Refresh(ctx); err != nil && onErr != nil {
				onErr(err)
			}
		}
	}
}
