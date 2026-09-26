package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/IniZio/nexus/internal/core/vault"
	"github.com/IniZio/nexus/internal/core/vault/connectors"
	"github.com/IniZio/nexus/internal/core/vault/linkflow"
)

func init() {
	Register(Command{
		Name:    "vault",
		Summary: "Manage linked integration credentials",
		Run:     runVault,
	})
}

func runVault(ctx context.Context, args []string, out *Output) error {
	if len(args) == 0 {
		return &UsageError{Msg: "vault: usage: vault <link|ls|rm> [args]"}
	}
	switch args[0] {
	case "link":
		return runVaultLink(ctx, args[1:], out)
	case "ls":
		return runVaultLs(ctx, args[1:], out)
	case "rm":
		return runVaultRm(ctx, args[1:], out)
	default:
		return &UsageError{Msg: fmt.Sprintf("vault: unknown subcommand %q; valid: link, ls, rm", args[0])}
	}
}

func runVaultLink(ctx context.Context, args []string, out *Output) error {
	if len(args) == 0 {
		return &UsageError{Msg: "vault link: usage: vault link <github|linear>"}
	}
	integration := strings.ToLower(args[0])

	st, err := vaultStore()
	if err != nil {
		return err
	}
	principal, err := vault.LocalPrincipal()
	if err != nil {
		return fmt.Errorf("vault link: %w", err)
	}
	key := vault.Key{Principal: principal, Integration: integration}

	switch integration {
	case "github":
		return runVaultLinkGitHub(ctx, key, st, out)
	case "linear":
		return runVaultLinkLinear(ctx, key, st, out)
	default:
		return &UsageError{Msg: fmt.Sprintf("vault link: unknown integration %q; supported: github, linear", integration)}
	}
}

func runVaultLinkGitHub(ctx context.Context, key vault.Key, st vault.Store, out *Output) error {
	cfg, err := vaultAppConfig()
	if err != nil {
		return err
	}
	clientID := cfg.GitHub.ClientID
	if clientID == "" {
		clientID = connectors.DefaultGitHubClientID
	}
	conn := connectors.NewGitHub(clientID)

	auth, err := conn.StartDevice(ctx)
	if err != nil {
		return fmt.Errorf("vault link github: start device flow: %w", err)
	}
	fmt.Fprintf(out.Stdout(), "Open: %s\nCode: %s\n", auth.VerificationURI, auth.UserCode)

	interval := time.Duration(auth.Interval) * time.Second
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
		rec, pollErr := conn.PollDevice(ctx, auth.DeviceCode)
		if errors.Is(pollErr, connectors.ErrAuthorizationPending) {
			continue
		}
		if errors.Is(pollErr, connectors.ErrSlowDown) {
			interval += 5 * time.Second
			continue
		}
		if pollErr != nil {
			return fmt.Errorf("vault link github: %w", pollErr)
		}
		rec.AllowedProjects = []string{"*"}
		if err := st.Put(ctx, key, rec); err != nil {
			return fmt.Errorf("vault link github: store credential: %w", err)
		}
		fmt.Fprintln(out.Stdout(), "github: linked")
		return nil
	}
}

func runVaultLinkLinear(ctx context.Context, key vault.Key, st vault.Store, out *Output) error {
	cfg, err := vaultAppConfig()
	if err != nil {
		return err
	}
	clientID, cfgErr := cfg.LinearClientID()
	if cfgErr != nil {
		return cfgErr
	}

	redirectURL := fmt.Sprintf("http://localhost:%d", linkflow.LoopbackPort)
	conn := connectors.NewLinear(clientID, cfg.Linear.ClientSecret, redirectURL)

	state := fmt.Sprintf("nexus-%d", time.Now().UnixNano())
	authURL, codeVerifier, err := conn.AuthURL(state)
	if err != nil {
		return fmt.Errorf("vault link linear: %w", err)
	}

	listener, err := linkflow.Listen(ctx, fmt.Sprintf(":%d", linkflow.LoopbackPort), state)
	if err != nil {
		return fmt.Errorf("vault link linear: start loopback listener: %w", err)
	}
	defer listener.Close()

	fmt.Fprintf(out.Stdout(), "Open: %s\n", authURL)

	code, err := listener.Wait(ctx)
	if err != nil {
		return fmt.Errorf("vault link linear: waiting for callback: %w", err)
	}

	rec, err := conn.Exchange(ctx, code, codeVerifier)
	if err != nil {
		return fmt.Errorf("vault link linear: exchange: %w", err)
	}
	rec.AllowedProjects = []string{"*"}
	if err := st.Put(ctx, key, rec); err != nil {
		return fmt.Errorf("vault link linear: store credential: %w", err)
	}
	fmt.Fprintln(out.Stdout(), "linear: linked")
	return nil
}

func runVaultLs(ctx context.Context, _ []string, out *Output) error {
	st, err := vaultStore()
	if err != nil {
		return err
	}
	keys, err := st.List(ctx)
	if err != nil {
		return fmt.Errorf("vault ls: %w", err)
	}
	for _, k := range keys {
		fmt.Fprintf(out.Stdout(), "%s\t%s\n", k.Principal, k.Integration)
	}
	return nil
}

func runVaultRm(ctx context.Context, args []string, out *Output) error {
	if len(args) == 0 {
		return &UsageError{Msg: "vault rm: usage: vault rm <integration>"}
	}
	integration := strings.ToLower(args[0])
	st, err := vaultStore()
	if err != nil {
		return err
	}
	principal, err := vault.LocalPrincipal()
	if err != nil {
		return fmt.Errorf("vault rm: %w", err)
	}
	if err := st.Delete(ctx, vault.Key{Principal: principal, Integration: integration}); err != nil {
		return fmt.Errorf("vault rm: %w", err)
	}
	fmt.Fprintf(out.Stdout(), "removed: %s\n", integration)
	return nil
}

func vaultStoreDir() (string, error) {
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("vault: home dir: %w", err)
		}
		xdg = filepath.Join(home, ".config")
	}
	return filepath.Join(xdg, "nexus", "vault"), nil
}

func vaultStore() (vault.Store, error) {
	dir, err := vaultStoreDir()
	if err != nil {
		return nil, err
	}
	key, err := vault.LoadKey(nil)
	if err != nil {
		return nil, fmt.Errorf("vault: load key: %w", err)
	}
	return vault.NewFileStore(dir, key)
}

func vaultAppConfig() (vault.AppConfig, error) {
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return vault.AppConfig{}, fmt.Errorf("vault: home dir: %w", err)
		}
		xdg = filepath.Join(home, ".config")
	}
	return vault.LoadAppConfig(filepath.Join(xdg, "nexus"))
}
