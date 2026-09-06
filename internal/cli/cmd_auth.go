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

// runAuthLogin is the implementation of `nexus3 auth login`.
//
// Without --agent it behaves identically to the legacy single-route
// implementation: import a Claude Code .credentials.json into nexus3's
// dedicated credential store.
//
// With --agent it is profile-driven (D-MAC-14):
//   - rotating-chain agents (CredentialFormatNone, e.g. claude-code): import
//     the credential file and save it into nexus3's per-agent store. The store
//     is the source of truth; a host-side Refresher rotates it.  The default
//     --from path and the import function are both derived from [cred.OAuthImportReg];
//     no agent name appears here.
//   - static, read-only agents (CredentialFormatCursorJWT, e.g. cursor):
//     verify the credential is present and parseable, then report metadata
//     only — nothing is written. The supervisor reads the operator's file live
//     at boot (D-MAC-01); a copy taken here would go stale the moment the
//     operator re-logs in, and nexus3 would silently broker a dead token.
func runAuthLogin(_ context.Context, args []string, out *Output) error {
	fs := flag.NewFlagSet("auth login", flag.ContinueOnError)
	// Default is empty; the profile-derived default is filled in below after
	// flags are parsed and the profile is known.
	fromPath := fs.String("from", "", "source credential file path (default: agent-specific)")
	force := fs.Bool("force", false, "allow overwriting an existing complete credential store")
	agentName := fs.String("agent", "", "agent to authenticate (omit for claude-code default)")
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "auth login: " + err.Error()}
	}

	// oauthImport resolves the default --from path and the import function for
	// a given OAuth/rotating-chain profile via the cred registry, then calls
	// runAuthLoginImport.  All agent names are kept out of this file.
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

	// ── no --agent: preserve legacy claude-code behavior exactly ─────────────
	//
	// Operators and scripts depend on this path; its dest, import logic, and
	// --force guard must remain byte-identical to the pre-flag implementation.
	if *agentName == "" {
		return oauthImport(cred.ClaudeCodeProfile)
	}

	// ── profile-driven: resolve profile, then dispatch ────────────────────────
	profile, ok := cred.ProfileByName(*agentName)
	if !ok {
		return &UsageError{Msg: fmt.Sprintf(
			"auth login: unknown --agent %q; valid: %s",
			*agentName, strings.Join(cred.ProfileNames(), ", "),
		)}
	}

	// Dispatch is registry-driven: a profile with an ImportFromPathFn registered
	// (via OAuthImportReg) takes the import route; one without takes the
	// verify-and-report route.  This generalises across all OAuth/rotating-chain
	// formats, not just CredentialFormatNone, so a second OAuth agent with a
	// distinct format constant is automatically handled correctly.
	if _, _, hasImport := cred.OAuthImportReg(profile); hasImport {
		return oauthImport(profile)
	}
	// Static file credential (e.g. cursor-jwt): verify and report only.
	// Never import — the supervisor reads the file live (D-MAC-01 / D-MAC-14).
	return runAuthLoginVerify(profile, out)
}

// runAuthLoginImport handles the import path for rotating-chain agents
// (e.g. claude-code). It calls importFn to read the on-disk credential at
// fromPath, saves it into nexus3's per-agent store at dest, and reports
// metadata without printing token values.
//
// importFn is supplied by the caller from [cred.OAuthImportReg], keeping all
// per-agent knowledge in the cred registry and out of this function.
func runAuthLoginImport(importFn func(string) (*cred.DedicatedCredStore, error), fromPath string, force bool, dest string, out *Output) error {
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
//
// This function is profile-driven: it names no agent by name and branches on
// no CredentialFormat.  A new file-based agent requires no change here.
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
