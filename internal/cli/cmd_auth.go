package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/service"
)

func init() {
	Register(Command{
		Name:    "auth",
		Summary: "Manage Anthropic authentication",
		Run:     runAuth,
	})
}

// ── JSON data types ───────────────────────────────────────────────────────────

type authLoginJSON struct {
	DestPath      string `json:"dest_path"`
	TokenEndpoint string `json:"token_endpoint"`
	ClientID      string `json:"client_id"`
	ExpiresAt     string `json:"expires_at"`
}

type authVerifyJSON struct {
	CredPath  string `json:"cred_path"`
	ExpiresAt string `json:"expires_at"`
}

// ── runAuth ───────────────────────────────────────────────────────────────────

func runAuth(ctx context.Context, args []string, out *Output) error {
	if len(args) == 0 {
		return &UsageError{Msg: "auth: missing action; usage: auth <login>"}
	}

	action := args[0]
	actionArgs := args[1:]

	switch action {
	case "login":
		return runAuthLogin(ctx, actionArgs, out)
	default:
		return &UsageError{Msg: fmt.Sprintf("auth: unknown action %q; valid: login", action)}
	}
}

// runAuthLogin implements `nexus3 auth login` (D-MAC-14).
func runAuthLogin(_ context.Context, args []string, out *Output) error {
	fs := flag.NewFlagSet("auth login", flag.ContinueOnError)
	fromPath := fs.String("from", "", "source credential file path (default: agent-specific)")
	force := fs.Bool("force", false, "allow overwriting an existing complete credential store")
	agentName := fs.String("agent", "", "agent to authenticate (omit for claude-code default)")
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "auth login: " + err.Error()}
	}

	oauthImport := func(profile cred.AgentProfile) error {
		defaultFrom, importFn, ok := cred.OAuthImportReg(profile)
		if !ok {
			return fmt.Errorf("auth login: --agent %s: no import registration (programming error)", profile.Name)
		}
		from := *fromPath
		if from == "" {
			from = defaultFrom
		}
		dest := service.DedicatedCredStorePathForProfile(profile)
		return runAuthLoginImport(importFn, from, *force, dest, out)
	}

	// claude-code now uses live virtiofs mount (D-MAC-01)
	if *agentName == "" {
		fmt.Fprintf(out.Stdout(), "nexus3 auth login for claude-code is no longer needed.\n\n"+
			"Credentials are now managed via a live virtiofs mount of the host's\n"+
			"~/.claude directory into every claude-code sandbox.\n\n"+
			"To authenticate on the host, run:\n    claude login\n\n"+
			"The sandbox will pick up the credentials automatically on its next create.\n")
		return nil
	}

	profile, ok := cred.ProfileByName(*agentName)
	if !ok {
		return &UsageError{Msg: fmt.Sprintf(
			"auth login: unknown --agent %q; valid: %s",
			*agentName, strings.Join(cred.ProfileNames(), ", "),
		)}
	}

	// Registry-driven dispatch (OAuthImportReg)
	if profile.Capabilities.CredDirLiveMount {
		fmt.Fprintf(out.Stdout(), "nexus3 auth login for %s is no longer needed.\n\n"+
			"Credentials are now managed via a live virtiofs mount of the host's\n"+
			"~/.claude directory into every %s sandbox.\n\n"+
			"To authenticate on the host, run:\n    claude login\n\n"+
			"The sandbox will pick up the credentials automatically on its next create.\n",
			profile.Name, profile.Name)
		return nil
	}
	if _, _, hasImport := cred.OAuthImportReg(profile); hasImport {
		return oauthImport(profile)
	}
	// Verify only — supervisor reads file live (D-MAC-01)
	return runAuthLoginVerify(profile, out)
}

// runAuthLoginImport imports rotating-chain credentials via registry-supplied importFn.
func runAuthLoginImport(importFn func(string) (*cred.DedicatedCredStore, error), fromPath string, force bool, dest string, out *Output) error {
	if !force {
		existing, err := cred.LoadStore(dest)
		if err == nil && existing.RefreshToken != "" {
			return fmt.Errorf(
				"auth login: already authenticated at %s; re-import would overwrite the live credential chain; pass --force to override",
				dest,
			)
		}
	}

	store, err := importFn(fromPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf(
				"auth login: source credentials file not found: %s\n"+
					"Establish a dedicated session first:\n"+
					"  CLAUDE_CONFIG_DIR=~/.config/nexus3/claude-dedicated claude auth login",
				fromPath,
			)
		}
		return fmt.Errorf("auth login: importing credentials: %w", err)
	}

	if err := cred.SaveStore(dest, store); err != nil {
		return fmt.Errorf("auth login: saving credential store: %w", err)
	}

	data := authLoginJSON{
		DestPath:      dest,
		TokenEndpoint: store.TokenEndpoint,
		ClientID:      store.ClientID,
		ExpiresAt:     store.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
	msg := fmt.Sprintf(
		"auth login: credentials imported\n  store:          %s\n  token_endpoint: %s\n  client_id:      %s\n  expires_at:     %s",
		data.DestPath, data.TokenEndpoint, data.ClientID, data.ExpiresAt,
	)
	out.EmitSuccess("auth.login", data, msg)
	return nil
}

// runAuthLoginVerify verifies static-credential agents without importing (D-MAC-01).
func runAuthLoginVerify(profile cred.AgentProfile, out *Output) error {
	credPath, err := cred.StaticCredFilePath(profile)
	if err != nil {
		return fmt.Errorf("auth login: resolving %s credential path: %w", profile.Name, err)
	}

	store, err := cred.ImportCred(profile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf(
				"auth login: %s credential file not found: %s\n"+
					"Log in to %s first, then re-run this command.",
				profile.Name, credPath, profile.Name,
			)
		}
		return fmt.Errorf("auth login: verifying %s credential: %w", profile.Name, err)
	}

	expiresAt := "unknown"
	if !store.ExpiresAt.IsZero() {
		expiresAt = store.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z")
	}

	data := authVerifyJSON{
		CredPath:  credPath,
		ExpiresAt: expiresAt,
	}
	msg := fmt.Sprintf(
		"auth login: %s credential verified\n  path:       %s\n  expires_at: %s",
		profile.Name, data.CredPath, data.ExpiresAt,
	)
	out.EmitSuccess("auth.login", data, msg)
	return nil
}
