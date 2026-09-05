package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

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

// defaultFromPath returns the default source .credentials.json path:
// ~/.config/nexus3/claude-dedicated/.credentials.json.
func defaultFromPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "nexus3", "claude-dedicated", ".credentials.json")
}

// runAuthLogin is the implementation of `nexus3 auth login`.
//
// This command is bootstrap-only: it reads a Claude Code .credentials.json
// (from a dedicated session), imports it into nexus3's dedicated credential
// store, and reports metadata without printing token values.
//
// It deliberately refuses to overwrite an existing complete store (access +
// refresh token) unless --force is set. This prevents accidental downgrades
// to a stale token when the live credential chain is already healthy.
func runAuthLogin(_ context.Context, args []string, out *Output) error {
	fs := flag.NewFlagSet("auth login", flag.ContinueOnError)
	fromPath := fs.String("from", defaultFromPath(), "source Claude Code .credentials.json path")
	force := fs.Bool("force", false, "allow overwriting an existing complete credential store")
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "auth login: " + err.Error()}
	}

	dest := service.DedicatedCredStorePathForProfile(cred.ClaudeCodeProfile)

	// Guard: refuse to overwrite a live (complete) credential store unless
	//    --force is set.
	if !*force {
		existing, err := cred.LoadStore(dest)
		if err == nil && existing.RefreshToken != "" {
			return fmt.Errorf(
				"auth login: already authenticated at %s; re-import would overwrite the live credential chain; pass --force to override",
				dest,
			)
		}
		// ErrStoreAbsent → ok to proceed; other errors → ok to proceed (e.g.
		// malformed store is not "live").
	}

	store, err := cred.ImportClaudeCredentials(*fromPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf(
				"auth login: source credentials file not found: %s\n"+
					"Establish a dedicated session first:\n"+
					"  CLAUDE_CONFIG_DIR=~/.config/nexus3/claude-dedicated claude auth login",
				*fromPath,
			)
		}
		return fmt.Errorf("auth login: importing credentials: %w", err)
	}

	if err := cred.SaveStore(dest, store); err != nil {
		return fmt.Errorf("auth login: saving credential store: %w", err)
	}

	// Report success (no token values printed).
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
