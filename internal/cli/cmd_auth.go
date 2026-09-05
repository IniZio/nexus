package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
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

// defaultFromPath returns the default source .credentials.json path:
// ~/.config/nexus3/claude-dedicated/.credentials.json.
func defaultFromPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "nexus3", "claude-dedicated", ".credentials.json")
}

// runAuthLogin is the implementation of `nexus3 auth login`.
//
// Without --agent it behaves identically to the legacy single-route
// implementation: import a Claude Code .credentials.json into nexus3's
// dedicated credential store.
//
// With --agent it is profile-driven (D-MAC-14):
//   - rotating-chain agents (CredentialFormatNone, e.g. claude-code): import
//     the credential file and save it into nexus3's per-agent store. The store
//     is the source of truth; a host-side Refresher rotates it.
//   - static, read-only agents (CredentialFormatCursorJWT, e.g. cursor):
//     verify the credential is present and parseable, then report metadata
//     only — nothing is written. The supervisor reads the operator's file live
//     at boot (D-MAC-01); a copy taken here would go stale the moment the
//     operator re-logs in, and nexus3 would silently broker a dead token.
func runAuthLogin(_ context.Context, args []string, out *Output) error {
	fs := flag.NewFlagSet("auth login", flag.ContinueOnError)
	fromPath := fs.String("from", defaultFromPath(), "source Claude Code .credentials.json path")
	force := fs.Bool("force", false, "allow overwriting an existing complete credential store")
	agentName := fs.String("agent", "", "agent to authenticate (omit for claude-code default)")
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "auth login: " + err.Error()}
	}

	// ── no --agent: preserve legacy claude-code behavior exactly ─────────────
	//
	// Operators and scripts depend on this path; its dest, import logic, and
	// --force guard must remain byte-identical to the pre-flag implementation.
	if *agentName == "" {
		dest := service.DedicatedCredStorePathForProfile(cred.ClaudeCodeProfile)
		return runAuthLoginImport(*fromPath, *force, dest, out)
	}

	// ── profile-driven: resolve profile, then dispatch ────────────────────────
	profile, ok := cred.ProfileByName(*agentName)
	if !ok {
		return &UsageError{Msg: fmt.Sprintf(
			"auth login: unknown --agent %q; valid: %s",
			*agentName, strings.Join(cred.ProfileNames(), ", "),
		)}
	}

	switch profile.CredentialFormat {
	case cred.CredentialFormatNone:
		// OAuth / rotating-chain agent: same import path as the legacy route.
		dest := service.DedicatedCredStorePathForProfile(profile)
		return runAuthLoginImport(*fromPath, *force, dest, out)
	default:
		// Static file credential (e.g. cursor-jwt): verify and report only.
		// Never import — the supervisor reads the file live (D-MAC-01 / D-MAC-14).
		return runAuthLoginVerify(profile, out)
	}
}

// runAuthLoginImport handles the import path for rotating-chain agents
// (e.g. claude-code). It reads a Claude Code .credentials.json, saves it into
// nexus3's per-agent store at dest, and reports metadata without printing
// token values.
//
// It is called both by the legacy no-agent route and by --agent for profiles
// whose CredentialFormat is CredentialFormatNone.
func runAuthLoginImport(fromPath string, force bool, dest string, out *Output) error {
	// Guard: refuse to overwrite a live (complete) credential store unless
	//    --force is set.
	if !force {
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

	store, err := cred.ImportClaudeCredentials(fromPath)
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

// runAuthLoginVerify handles the verify-and-report path for static-credential
// agents (e.g. cursor). It reads the operator's credential file (read-only),
// confirms it is present and parseable, and reports metadata. It writes nothing.
//
// The supervisor reads the operator's file live at boot (D-MAC-01 / D-MAC-14);
// importing and saving a copy here would silently broker a stale token the
// moment the operator re-logs in to the agent.
func runAuthLoginVerify(profile cred.AgentProfile, out *Output) error {
	credPath, err := cred.CursorCredPath(profile)
	if err != nil {
		return fmt.Errorf("auth login: resolving %s credential path: %w", profile.Name, err)
	}

	store, err := cred.ImportCursorCredentials(profile)
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
